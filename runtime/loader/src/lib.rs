#![cfg(windows)]
#![allow(non_snake_case)]

use std::ffi::{c_char, c_void};
use std::mem;
use std::ptr::{copy_nonoverlapping, null, null_mut};
use std::sync::atomic::{AtomicBool, AtomicPtr, Ordering};

type Bool = i32;
type Dword = u32;
type Handle = *mut c_void;
type Hmodule = *mut c_void;

const DLL_PROCESS_ATTACH: Dword = 1;
const PAGE_READWRITE: Dword = 0x04;
const PAGE_EXECUTE_READWRITE: Dword = 0x40;
const MEM_COMMIT: Dword = 0x0000_1000;
const MEM_RESERVE: Dword = 0x0000_2000;
const FILE_READ_ATTRIBUTES: Dword = 0x80;
const FILE_SHARE_READ_WRITE_DELETE: Dword = 0x7;
const OPEN_EXISTING: Dword = 3;
const FILE_STAT_OPEN_FLAGS: Dword = 0x0220_0000;
const IMAGE_SCN_MEM_EXECUTE: Dword = 0x2000_0000;

// 14-byte position-independent absolute jump: `jmp qword ptr [rip+0]` + 8-byte target.
const JMP_LEN: usize = 14;

type CreateFileW = unsafe extern "system" fn(
    *const u16,
    Dword,
    Dword,
    *mut c_void,
    Dword,
    Dword,
    Handle,
) -> Handle;
type GetProcAddressFn = unsafe extern "system" fn(Hmodule, *const c_char) -> *mut c_void;

// The original QQNT.dll IAT entry. Before QQNT loads, this holds the system
// CreateFileW entry so the path-based stat shim remains callable.
static ORIGINAL_CREATE_FILE_W: AtomicPtr<c_void> = AtomicPtr::new(null_mut());
static ORIGINAL_GET_PROC_ADDRESS: AtomicPtr<c_void> = AtomicPtr::new(null_mut());
static QQNT_PATCHED: AtomicBool = AtomicBool::new(false);
static HEADLESS: AtomicBool = AtomicBool::new(false);
static ORIGINAL_CREATE_WINDOW: AtomicPtr<c_void> = AtomicPtr::new(null_mut());
static ORIGINAL_SHOW_WINDOW: AtomicPtr<c_void> = AtomicPtr::new(null_mut());
static ORIGINAL_SET_WINDOW_POS: AtomicPtr<c_void> = AtomicPtr::new(null_mut());
static ORIGINAL_SET_WINDOW_PLACEMENT: AtomicPtr<c_void> = AtomicPtr::new(null_mut());
const WS_CHILD: u32 = 0x4000_0000;
const WS_VISIBLE: u32 = 0x1000_0000;
const SWP_SHOWWINDOW: u32 = 0x40;

#[link(name = "user32")]
extern "system" {
    fn GetWindowLongW(window: Handle, index: i32) -> i32;
}

#[link(name = "kernel32")]
extern "system" {
    fn CloseHandle(handle: Handle) -> Bool;
    fn DisableThreadLibraryCalls(module: Hmodule) -> Bool;
    fn FlushInstructionCache(process: Handle, address: *const c_void, size: usize) -> Bool;
    fn GetFileInformationByHandleEx(
        file: Handle,
        info_class: i32,
        buffer: *mut c_void,
        buffer_size: Dword,
    ) -> Bool;
    fn GetCurrentProcess() -> Handle;
    fn GetEnvironmentVariableW(name: *const u16, value: *mut u16, size: Dword) -> Dword;
    fn GetModuleHandleA(name: *const c_char) -> Hmodule;
    fn GetProcAddress(module: Hmodule, name: *const c_char) -> *mut c_void;
    fn LoadLibraryW(name: *const u16) -> Hmodule;
    fn CreateFileW(
        name: *const u16,
        access: Dword,
        share: Dword,
        security: *mut c_void,
        disposition: Dword,
        flags: Dword,
        template: Handle,
    ) -> Handle;
    fn WriteFile(
        file: Handle,
        buffer: *const u8,
        size: Dword,
        written: *mut Dword,
        overlapped: *mut c_void,
    ) -> Bool;
    fn VirtualAlloc(
        address: *mut c_void,
        size: usize,
        allocation: Dword,
        protect: Dword,
    ) -> *mut c_void;
    fn VirtualProtect(
        address: *mut c_void,
        size: usize,
        protection: Dword,
        old: *mut Dword,
    ) -> Bool;
}

#[no_mangle]
/// # Safety
///
/// This function is called by the Windows loader with a valid module handle
/// and the standard `DllMain` arguments.
pub unsafe extern "system" fn DllMain(
    module: Hmodule,
    reason: Dword,
    _reserved: *mut c_void,
) -> Bool {
    if reason == DLL_PROCESS_ATTACH {
        DisableThreadLibraryCalls(module);
        HEADLESS.store(
            read_environment("CINLAN_QQNT_HEADLESS")
                .map(|v| String::from_utf16_lossy(&v[..v.len() - 1]) == "true")
                .unwrap_or(false),
            Ordering::Release,
        );
        if !install_hook() {
            write_hook_status("error: startup IAT/stat hook failed");
            return 0;
        }
    }
    1
}

unsafe fn install_hook() -> bool {
    let main_module = GetModuleHandleA(null());
    if main_module.is_null() {
        return false;
    }

    let kernelbase = GetModuleHandleA(c"kernelbase.dll".as_ptr());
    if kernelbase.is_null() {
        return false;
    }
    let create_file = GetProcAddress(kernelbase, c"CreateFileW".as_ptr());
    if create_file.is_null() {
        return false;
    }
    ORIGINAL_CREATE_FILE_W.store(create_file, Ordering::Release);

    if let Some(original) = patch_iat(
        main_module,
        b"KERNEL32.dll\0",
        b"GetProcAddress\0",
        hook_get_proc_address as *mut c_void,
    ) {
        ORIGINAL_GET_PROC_ADDRESS.store(original, Ordering::Release);
    } else {
        return false;
    }

    // Windows 11 libuv resolves the patched main entry through this path-based
    // stat API. Keep the compatibility shim inline because it is dynamically
    // resolved and therefore has no stable QQ.exe IAT slot.
    let stat_target = GetProcAddress(kernelbase, c"GetFileInformationByName".as_ptr());
    if !stat_target.is_null()
        && inline_hook(
            stat_target as *mut u8,
            hook_get_file_information_by_name as *mut c_void,
        )
        .is_none()
    {
        return false;
    }
    true
}

unsafe extern "system" fn hook_get_proc_address(
    module: Hmodule,
    symbol: *const c_char,
) -> *mut c_void {
    let original = ORIGINAL_GET_PROC_ADDRESS.load(Ordering::Acquire);
    if original.is_null() {
        return null_mut();
    }
    if !module.is_null()
        && symbol as usize > u16::MAX as usize
        && ascii_equal(symbol.cast(), b"ExportedContentMain\0")
        && !patch_qqnt(module)
    {
        return null_mut();
    }
    let call: GetProcAddressFn = mem::transmute(original);
    call(module, symbol)
}

unsafe fn patch_qqnt(module: Hmodule) -> bool {
    if QQNT_PATCHED.load(Ordering::Acquire) {
        return true;
    }
    let guard = match patch_content_guard(module) {
        Some(address) => address,
        None => {
            QQNT_PATCHED.store(false, Ordering::Release);
            write_hook_status("error: QQNT startup guard signature/patch failed");
            return false;
        }
    };

    match patch_iat(
        module,
        b"KERNEL32.dll\0",
        b"CreateFileW\0",
        hook_create_file_w as *mut c_void,
    ) {
        Some(original) => {
            ORIGINAL_CREATE_FILE_W.store(original, Ordering::Release);
            if HEADLESS.load(Ordering::Acquire) && !install_window_hooks(module) {
                restore_content_guard(guard);
                write_hook_status("error: QQNT headless delay-IAT patch failed");
                return false;
            }
            write_hook_status("ready");
            QQNT_PATCHED.store(true, Ordering::Release);
            true
        }
        None => {
            restore_content_guard(guard);
            QQNT_PATCHED.store(false, Ordering::Release);
            write_hook_status("error: QQNT CreateFileW IAT patch failed");
            false
        }
    }
}

unsafe fn write_hook_status(message: &str) {
    let Some(path) = read_environment("CINLAN_QQNT_HOOK_STATUS_PATH") else {
        return;
    };
    let file = CreateFileW(
        path.as_ptr(),
        0x4000_0000,
        7,
        null_mut(),
        2,
        0x80,
        null_mut(),
    );
    if file == (-1_isize) as Handle {
        return;
    }
    let mut written = 0;
    WriteFile(
        file,
        message.as_ptr(),
        message.len() as u32,
        &mut written,
        null_mut(),
    );
    CloseHandle(file);
}

fn suppress_window(headless: bool, style: u32) -> bool {
    headless && style & WS_CHILD == 0
}
fn creation_style(headless: bool, style: u32) -> u32 {
    if suppress_window(headless, style) {
        style & !WS_VISIBLE
    } else {
        style
    }
}

// QQNT imports USER32 through the PE delay-import table, not the ordinary IAT.
// Resolve originals from USER32 and replace all relevant slots before ContentMain.
unsafe fn install_window_hooks(module: Hmodule) -> bool {
    let user32: Vec<u16> = "user32.dll".encode_utf16().chain(Some(0)).collect();
    let user32 = LoadLibraryW(user32.as_ptr());
    if user32.is_null() {
        return false;
    }
    for (name, detour, original) in [
        (
            c"CreateWindowExW",
            hook_create_window as *mut c_void,
            &ORIGINAL_CREATE_WINDOW,
        ),
        (
            c"ShowWindow",
            hook_show_window as *mut c_void,
            &ORIGINAL_SHOW_WINDOW,
        ),
        (
            c"SetWindowPos",
            hook_set_window_pos as *mut c_void,
            &ORIGINAL_SET_WINDOW_POS,
        ),
        (
            c"SetWindowPlacement",
            hook_set_window_placement as *mut c_void,
            &ORIGINAL_SET_WINDOW_PLACEMENT,
        ),
    ] {
        let pointer = GetProcAddress(user32, name.as_ptr());
        if pointer.is_null() {
            return false;
        }
        original.store(pointer, Ordering::Release);
        if !patch_delay_iat(module, name.to_bytes_with_nul(), detour) {
            return false;
        }
    }
    // Include dynamically resolved USER32 calls as well as delay-IAT calls.
    if patch_iat(
        module,
        b"KERNEL32.dll\0",
        b"GetProcAddress\0",
        hook_window_get_proc_address as *mut c_void,
    )
    .is_none()
    {
        return false;
    }
    true
}

unsafe extern "system" fn hook_window_get_proc_address(
    module: Hmodule,
    symbol: *const c_char,
) -> *mut c_void {
    let call: GetProcAddressFn = mem::transmute(ORIGINAL_GET_PROC_ADDRESS.load(Ordering::Acquire));
    let user32 = GetModuleHandleA(c"user32.dll".as_ptr());
    if HEADLESS.load(Ordering::Acquire)
        && !user32.is_null()
        && module == user32
        && symbol as usize > u16::MAX as usize
    {
        for (name, hook) in [
            (
                b"CreateWindowExW\0".as_slice(),
                hook_create_window as *mut c_void,
            ),
            (b"ShowWindow\0".as_slice(), hook_show_window as *mut c_void),
            (
                b"SetWindowPos\0".as_slice(),
                hook_set_window_pos as *mut c_void,
            ),
            (
                b"SetWindowPlacement\0".as_slice(),
                hook_set_window_placement as *mut c_void,
            ),
        ] {
            if ascii_equal(symbol.cast(), name) {
                return hook;
            }
        }
    }
    call(module, symbol)
}

unsafe fn patch_delay_iat(module: Hmodule, symbol: &[u8], replacement: *mut c_void) -> bool {
    let base = module as *mut u8;
    let nt = read_u32(base.add(0x3c)) as usize;
    let optional = base.add(nt + 24);
    let rva = read_u32(optional.add(112 + 13 * 8)) as usize;
    if rva == 0 {
        return false;
    }
    let mut d = base.add(rva);
    while read_u32(d.add(4)) != 0 {
        if read_u32(d) != 1 {
            return false;
        } // Only RVA-based x64 descriptors.
        if ascii_equal_ci(base.add(read_u32(d.add(4)) as usize), b"USER32.dll\0") {
            let names = read_u32(d.add(16)) as usize;
            let slots = read_u32(d.add(12)) as usize;
            let mut i = 0;
            loop {
                let name = read_u64(base.add(names + i * 8));
                if name == 0 {
                    break;
                }
                if name & (1 << 63) == 0 && ascii_equal(base.add(name as usize + 2), symbol) {
                    let slot = base.add(slots + i * 8) as *mut *mut c_void;
                    let mut old = 0;
                    if VirtualProtect(slot.cast(), 8, PAGE_READWRITE, &mut old) == 0 {
                        return false;
                    }
                    slot.write(replacement);
                    let mut ignored = 0;
                    VirtualProtect(slot.cast(), 8, old, &mut ignored);
                    return true;
                }
                i += 1;
            }
        }
        d = d.add(32);
    }
    false
}

unsafe extern "system" fn hook_create_window(
    ex: u32,
    class: *const u16,
    name: *const u16,
    style: u32,
    x: i32,
    y: i32,
    w: i32,
    h: i32,
    parent: Handle,
    menu: Handle,
    instance: Handle,
    param: *mut c_void,
) -> Handle {
    let call: unsafe extern "system" fn(
        u32,
        *const u16,
        *const u16,
        u32,
        i32,
        i32,
        i32,
        i32,
        Handle,
        Handle,
        Handle,
        *mut c_void,
    ) -> Handle = mem::transmute(ORIGINAL_CREATE_WINDOW.load(Ordering::Acquire));
    call(
        ex,
        class,
        name,
        creation_style(HEADLESS.load(Ordering::Acquire), style),
        x,
        y,
        w,
        h,
        parent,
        menu,
        instance,
        param,
    )
}
unsafe extern "system" fn hook_show_window(window: Handle, command: i32) -> Bool {
    let call: unsafe extern "system" fn(Handle, i32) -> Bool =
        mem::transmute(ORIGINAL_SHOW_WINDOW.load(Ordering::Acquire));
    let hide = suppress_window(
        HEADLESS.load(Ordering::Acquire),
        GetWindowLongW(window, -16) as u32,
    );
    call(window, if hide { 0 } else { command })
}
unsafe extern "system" fn hook_set_window_pos(
    window: Handle,
    after: Handle,
    x: i32,
    y: i32,
    w: i32,
    h: i32,
    flags: u32,
) -> Bool {
    let call: unsafe extern "system" fn(Handle, Handle, i32, i32, i32, i32, u32) -> Bool =
        mem::transmute(ORIGINAL_SET_WINDOW_POS.load(Ordering::Acquire));
    let hide = suppress_window(
        HEADLESS.load(Ordering::Acquire),
        GetWindowLongW(window, -16) as u32,
    );
    call(
        window,
        after,
        x,
        y,
        w,
        h,
        if hide { flags & !SWP_SHOWWINDOW } else { flags },
    )
}
#[repr(C)]
#[derive(Clone, Copy)]
struct WindowPlacement {
    length: u32,
    flags: u32,
    show_cmd: u32,
    min_position: [i32; 2],
    max_position: [i32; 2],
    normal_rect: [i32; 4],
}
unsafe extern "system" fn hook_set_window_placement(
    window: Handle,
    placement: *const WindowPlacement,
) -> Bool {
    let call: unsafe extern "system" fn(Handle, *const WindowPlacement) -> Bool =
        mem::transmute(ORIGINAL_SET_WINDOW_PLACEMENT.load(Ordering::Acquire));
    if !placement.is_null()
        && suppress_window(
            HEADLESS.load(Ordering::Acquire),
            GetWindowLongW(window, -16) as u32,
        )
    {
        let mut hidden = *placement;
        hidden.show_cmd = 0;
        call(window, &hidden)
    } else {
        call(window, placement)
    }
}

unsafe extern "system" fn hook_create_file_w(
    filename: *const u16,
    desired_access: Dword,
    share_mode: Dword,
    security_attributes: *mut c_void,
    creation_disposition: Dword,
    flags_and_attributes: Dword,
    template_file: Handle,
) -> Handle {
    let original = ORIGINAL_CREATE_FILE_W.load(Ordering::Acquire);
    if original.is_null() {
        return (-1_isize) as Handle;
    }
    let call: CreateFileW = mem::transmute(original);

    let redirected = redirect_for(filename);
    let selected = redirected.as_ref().map_or(filename, |value| value.as_ptr());
    call(
        selected,
        desired_access,
        share_mode,
        security_attributes,
        creation_disposition,
        flags_and_attributes,
        template_file,
    )
}

// Shared redirect decision for all file hooks: the in-memory-patched paths
// map to their env-provided replacements; everything else passes through.
unsafe fn redirect_for(filename: *const u16) -> Option<Vec<u16>> {
    if wide_path_ends_with(filename, r"\resources\app\package.json") {
        read_environment("CINLAN_QQNT_PATCH_PACKAGE")
    } else if wide_path_ends_with(filename, r"\resources\app\loadCinlan.js")
        || wide_path_ends_with(filename, r"\resources\app\application.asar\loadCinlan.js")
    {
        read_environment("CINLAN_QQNT_LOAD_PATH")
    } else {
        None
    }
}

// Path-based stat hook: this follows NapCat's observed compatibility shim.
// Rather than delegating to the native resolver, open the selected path and
// query the requested metadata through GetFileInformationByHandleEx. This is
// important for the in-memory loader path: Node/libuv can resolve it through
// GetFileInformationByName without ever reaching CreateFileW itself.
unsafe extern "system" fn hook_get_file_information_by_name(
    filename: *const u16,
    info_class: i32,
    file_info_buffer: *mut c_void,
    file_info_buffer_size: Dword,
) -> Bool {
    let original_open = ORIGINAL_CREATE_FILE_W.load(Ordering::Acquire);
    if original_open.is_null() {
        return 0;
    }
    let open: CreateFileW = mem::transmute(original_open);

    let redirected = redirect_for(filename);
    let selected = redirected.as_ref().map_or(filename, |value| value.as_ptr());
    let file = open(
        selected,
        FILE_READ_ATTRIBUTES,
        FILE_SHARE_READ_WRITE_DELETE,
        null_mut(),
        OPEN_EXISTING,
        FILE_STAT_OPEN_FLAGS,
        null_mut(),
    );
    if file == (-1_isize) as Handle {
        return 0;
    }
    let result =
        GetFileInformationByHandleEx(file, info_class, file_info_buffer, file_info_buffer_size);
    CloseHandle(file);
    result
}

unsafe fn patch_content_guard(module: Hmodule) -> Option<*mut u8> {
    if module.is_null() {
        return None;
    }
    let base = module as *mut u8;
    if read_u16(base) != 0x5a4d {
        return None;
    }
    let nt_offset = read_u32(base.add(0x3c)) as usize;
    if read_u32(base.add(nt_offset)) != 0x0000_4550 {
        return None;
    }

    let optional_header = base.add(nt_offset + 24);
    if read_u16(optional_header) != 0x20b {
        return None;
    }
    let image_size = read_u32(optional_header.add(56)) as usize;
    let section_count = read_u16(base.add(nt_offset + 6)) as usize;
    let optional_header_size = read_u16(base.add(nt_offset + 20)) as usize;
    let mut section = base.add(nt_offset + 24 + optional_header_size);

    for _ in 0..section_count {
        let characteristics = read_u32(section.add(36));
        if characteristics & IMAGE_SCN_MEM_EXECUTE != 0 {
            let virtual_address = read_u32(section.add(12)) as usize;
            let virtual_size = read_u32(section.add(8)) as usize;
            let raw_size = read_u32(section.add(16)) as usize;
            let section_size = if virtual_size == 0 {
                raw_size
            } else {
                virtual_size
            };
            let section_end = virtual_address.checked_add(section_size)?.min(image_size);
            if section_end >= virtual_address + 25 {
                for offset in virtual_address..=(section_end - 25) {
                    let candidate = std::slice::from_raw_parts(base.add(offset), 25);
                    if content_guard_pattern(candidate) {
                        let branch = base.add(offset + 12);
                        if set_content_guard_branch(branch, 0x85, 0x84) {
                            return Some(branch);
                        }
                        return None;
                    }
                }
            }
        }
        section = section.add(40);
    }
    None
}

fn content_guard_pattern(value: &[u8]) -> bool {
    value.len() >= 25
        && value[0] == 0xE8
        && value[5] == 0xE8
        && value[10] == 0x84
        && value[11] == 0xC0
        && value[12] == 0x0F
        && value[13] == 0x85
        && value[18] == 0x48
        && value[19] == 0x8D
        && value[20] == 0x0D
}

unsafe fn set_content_guard_branch(address: *mut u8, expected: u8, replacement: u8) -> bool {
    if address.read() != 0x0F || address.add(1).read() != expected {
        return false;
    }
    let mut old = 0;
    if VirtualProtect(address.cast(), 2, PAGE_EXECUTE_READWRITE, &mut old) == 0 {
        return false;
    }
    address.add(1).write(replacement);
    let mut ignored = 0;
    VirtualProtect(address.cast(), 2, old, &mut ignored);
    FlushInstructionCache(GetCurrentProcess(), address.cast(), 2);
    true
}

unsafe fn restore_content_guard(address: *mut u8) {
    let _ = set_content_guard_branch(address, 0x84, 0x85);
}

// Install a 14-byte absolute jump at `target`, relocating the overwritten
// prologue into a freshly allocated trampoline. Returns the trampoline pointer
// (the "call original" path) or None if the prologue cannot be relocated
// safely, in which case no bytes are modified.
unsafe fn inline_hook(target: *mut u8, detour: *mut c_void) -> Option<*mut c_void> {
    let mut stolen = 0usize;
    while stolen < JMP_LEN {
        let len = instruction_length(target.add(stolen))?;
        if len == 0 {
            return None;
        }
        stolen += len;
    }

    let trampoline = VirtualAlloc(
        null_mut(),
        stolen + JMP_LEN,
        MEM_COMMIT | MEM_RESERVE,
        PAGE_EXECUTE_READWRITE,
    ) as *mut u8;
    if trampoline.is_null() {
        return None;
    }
    copy_nonoverlapping(target as *const u8, trampoline, stolen);
    write_abs_jmp(trampoline.add(stolen), target as u64 + stolen as u64);

    let mut old: Dword = 0;
    if VirtualProtect(
        target as *mut c_void,
        stolen,
        PAGE_EXECUTE_READWRITE,
        &mut old,
    ) == 0
    {
        return None;
    }
    write_abs_jmp(target, detour as u64);
    let mut index = JMP_LEN;
    while index < stolen {
        target.add(index).write(0x90);
        index += 1;
    }
    let mut ignored: Dword = 0;
    VirtualProtect(target as *mut c_void, stolen, old, &mut ignored);
    FlushInstructionCache(GetCurrentProcess(), target as *const c_void, stolen);

    Some(trampoline as *mut c_void)
}

unsafe fn patch_iat(
    module: Hmodule,
    expected_dll: &[u8],
    expected_symbol: &[u8],
    replacement: *mut c_void,
) -> Option<*mut c_void> {
    if module.is_null() {
        return None;
    }
    let base = module as *mut u8;
    if read_u16(base) != 0x5a4d {
        return None;
    }
    let nt_offset = read_u32(base.add(0x3c)) as usize;
    if read_u32(base.add(nt_offset)) != 0x0000_4550 {
        return None;
    }

    let optional_header = base.add(nt_offset + 24);
    if read_u16(optional_header) != 0x20b {
        return None;
    }
    let import_rva = read_u32(optional_header.add(120)) as usize;
    if import_rva == 0 {
        return None;
    }

    let mut descriptor = base.add(import_rva);
    loop {
        let original_first_thunk = read_u32(descriptor) as usize;
        let name_rva = read_u32(descriptor.add(12)) as usize;
        let first_thunk = read_u32(descriptor.add(16)) as usize;
        if original_first_thunk == 0 && name_rva == 0 && first_thunk == 0 {
            return None;
        }

        if name_rva != 0 && ascii_equal_ci(base.add(name_rva), expected_dll) {
            let lookup_rva = if original_first_thunk != 0 {
                original_first_thunk
            } else {
                first_thunk
            };
            let mut index = 0usize;
            loop {
                let lookup = read_u64(base.add(lookup_rva + index * 8));
                if lookup == 0 {
                    break;
                }
                if lookup & (1_u64 << 63) == 0
                    && ascii_equal(base.add(lookup as usize + 2), expected_symbol)
                {
                    let slot = base.add(first_thunk + index * 8) as *mut *mut c_void;
                    let original = slot.read();
                    let mut old = 0;
                    if VirtualProtect(
                        slot.cast(),
                        mem::size_of::<*mut c_void>(),
                        PAGE_READWRITE,
                        &mut old,
                    ) == 0
                    {
                        return None;
                    }
                    slot.write(replacement);
                    FlushInstructionCache(
                        GetCurrentProcess(),
                        slot.cast(),
                        mem::size_of::<*mut c_void>(),
                    );
                    let mut ignored = 0;
                    VirtualProtect(
                        slot.cast(),
                        mem::size_of::<*mut c_void>(),
                        old,
                        &mut ignored,
                    );
                    return Some(original);
                }
                index += 1;
            }
        }
        descriptor = descriptor.add(20);
    }
}

unsafe fn write_abs_jmp(dst: *mut u8, target: u64) {
    // FF 25 00 00 00 00 -> jmp qword ptr [rip+0]; the 8-byte target follows inline.
    dst.add(0).write(0xFF);
    dst.add(1).write(0x25);
    dst.add(2).write(0x00);
    dst.add(3).write(0x00);
    dst.add(4).write(0x00);
    dst.add(5).write(0x00);
    (dst.add(6) as *mut u64).write_unaligned(target);
}

// Minimal x64 instruction-length decoder, scoped to the instruction forms that
// occur in Windows API prologues. Returns None (aborting the hook) for any
// opcode it does not recognise, or any RIP-relative operand / relative branch,
// because those cannot be copied to a different address without rewriting.
unsafe fn instruction_length(p: *const u8) -> Option<usize> {
    let mut i = 0usize;
    let mut operand_size_override = false;

    loop {
        match p.add(i).read() {
            0x66 => {
                operand_size_override = true;
                i += 1;
            }
            0x67 | 0xF0 | 0xF2 | 0xF3 | 0x2E | 0x36 | 0x3E | 0x26 | 0x64 | 0x65 => {
                i += 1;
            }
            _ => break,
        }
    }

    let mut rex_w = false;
    let candidate = p.add(i).read();
    if (0x40..=0x4F).contains(&candidate) {
        rex_w = candidate & 0x08 != 0;
        i += 1;
    }

    let opcode = p.add(i).read();
    i += 1;

    let (has_modrm, imm_size): (bool, usize) = match opcode {
        0x50..=0x5F => (false, 0),
        0x68 => (false, if operand_size_override { 2 } else { 4 }),
        0x6A => (false, 1),
        0x88 | 0x89 | 0x8A | 0x8B | 0x8D => (true, 0),
        0xC6 => (true, 1),
        0xC7 => (true, if operand_size_override { 2 } else { 4 }),
        0x80 => (true, 1),
        0x81 => (true, if operand_size_override { 2 } else { 4 }),
        0x83 => (true, 1),
        0x00 | 0x01 | 0x02 | 0x03 | 0x08 | 0x09 | 0x0A | 0x0B | 0x10 | 0x11 | 0x12 | 0x13
        | 0x18 | 0x19 | 0x1A | 0x1B | 0x20 | 0x21 | 0x22 | 0x23 | 0x28 | 0x29 | 0x2A | 0x2B
        | 0x30 | 0x31 | 0x32 | 0x33 | 0x38 | 0x39 | 0x3A | 0x3B | 0x84 | 0x85 | 0x86 | 0x87 => {
            (true, 0)
        }
        0xB0..=0xB7 => (false, 1),
        0xB8..=0xBF => (
            false,
            if rex_w {
                8
            } else if operand_size_override {
                2
            } else {
                4
            },
        ),
        0xFF => (true, 0),
        0x90 => (false, 0),
        0x0F => {
            let op2 = p.add(i).read();
            i += 1;
            match op2 {
                0x1F | 0xB6 | 0xB7 | 0xBE | 0xBF => (true, 0),
                _ => return None,
            }
        }
        _ => return None,
    };

    if has_modrm {
        let modrm = p.add(i).read();
        i += 1;
        let md = modrm >> 6;
        let rm = modrm & 0x07;
        if md != 3 {
            if md == 0 && rm == 5 {
                // [rip+disp32] — position dependent, cannot relocate.
                return None;
            }
            if rm == 4 {
                let sib = p.add(i).read();
                i += 1;
                if md == 0 && sib & 0x07 == 5 {
                    i += 4;
                } else if md == 1 {
                    i += 1;
                } else if md == 2 {
                    i += 4;
                }
            } else if md == 1 {
                i += 1;
            } else if md == 2 {
                i += 4;
            }
        }
    }

    Some(i + imm_size)
}

unsafe fn read_environment(name: &str) -> Option<Vec<u16>> {
    let key: Vec<u16> = name.encode_utf16().chain(Some(0)).collect();
    let mut value = vec![0_u16; 32_768];
    let length = GetEnvironmentVariableW(key.as_ptr(), value.as_mut_ptr(), value.len() as Dword);
    if length == 0 || length as usize >= value.len() {
        return None;
    }
    value.truncate(length as usize + 1);
    value[length as usize] = 0;
    Some(value)
}

unsafe fn wide_path_ends_with(value: *const u16, suffix: &str) -> bool {
    if value.is_null() {
        return false;
    }
    let mut length = 0usize;
    while length < 32_768 && value.add(length).read() != 0 {
        length += 1;
    }
    let suffix: Vec<u16> = suffix.encode_utf16().collect();
    if suffix.len() > length {
        return false;
    }
    for index in 0..suffix.len() {
        let left = normalize_wide(value.add(length - suffix.len() + index).read());
        let right = normalize_wide(suffix[index]);
        if left != right {
            return false;
        }
    }
    true
}

fn normalize_wide(value: u16) -> u16 {
    if value >= u16::from(b'A') && value <= u16::from(b'Z') {
        value + 32
    } else if value == u16::from(b'/') {
        u16::from(b'\\')
    } else {
        value
    }
}

unsafe fn ascii_equal(value: *const u8, expected: &[u8]) -> bool {
    for (index, right) in expected.iter().enumerate() {
        if value.add(index).read() != *right {
            return false;
        }
        if *right == 0 {
            return true;
        }
    }
    false
}

unsafe fn ascii_equal_ci(value: *const u8, expected: &[u8]) -> bool {
    for (index, right) in expected.iter().enumerate() {
        let left = value.add(index).read();
        if !left.eq_ignore_ascii_case(right) {
            return false;
        }
        if *right == 0 {
            return true;
        }
    }
    false
}

unsafe fn read_u16(address: *const u8) -> u16 {
    (address as *const u16).read_unaligned()
}

unsafe fn read_u32(address: *const u8) -> u32 {
    (address as *const u32).read_unaligned()
}

unsafe fn read_u64(address: *const u8) -> u64 {
    (address as *const u64).read_unaligned()
}

#[cfg(test)]
mod tests {
    use super::{
        content_guard_pattern, creation_style, suppress_window, wide_path_ends_with, WS_CHILD,
        WS_VISIBLE,
    };

    #[test]
    fn headless_preserves_children_and_default_behavior() {
        for style in [0, WS_VISIBLE, WS_CHILD, WS_CHILD | WS_VISIBLE] {
            assert_eq!(creation_style(false, style), style);
            assert!(!suppress_window(false, style));
        }
        assert_eq!(creation_style(true, WS_VISIBLE), 0);
        assert_eq!(
            creation_style(true, WS_CHILD | WS_VISIBLE),
            WS_CHILD | WS_VISIBLE
        );
        assert!(suppress_window(true, 0));
    }

    #[test]
    fn delay_iat_patch_requires_known_symbol_and_rva_descriptor() {
        let mut image = vec![0u8; 4096];
        image[0x3c..0x40].copy_from_slice(&0x80u32.to_le_bytes());
        let directory = 0x80 + 24 + 112 + 13 * 8;
        image[directory..directory + 4].copy_from_slice(&0x200u32.to_le_bytes());
        for (offset, value) in [
            (0x200, 1u32),
            (0x204, 0x280),
            (0x20c, 0x300),
            (0x210, 0x340),
        ] {
            image[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
        }
        image[0x280..0x28b].copy_from_slice(b"USER32.dll\0");
        image[0x340..0x348].copy_from_slice(&0x380u64.to_le_bytes());
        image[0x382..0x38d].copy_from_slice(b"ShowWindow\0");
        let module = image.as_mut_ptr().cast();
        let target = 0x1234usize as *mut std::ffi::c_void;
        assert!(!unsafe { super::patch_delay_iat(module, b"Unknown\0", target) });
        assert_eq!(unsafe { super::read_u64(image.as_ptr().add(0x300)) }, 0);
        assert!(unsafe { super::patch_delay_iat(module, b"ShowWindow\0", target) });
        assert_eq!(
            unsafe { super::read_u64(image.as_ptr().add(0x300)) },
            0x1234
        );
        image[0x200] = 0;
        assert!(!unsafe { super::patch_delay_iat(module, b"ShowWindow\0", target) });
    }

    fn wide_null(value: &str) -> Vec<u16> {
        value.encode_utf16().chain(Some(0)).collect()
    }

    #[test]
    fn target_paths_match_case_and_separator_independently() {
        for path in [
            r"D:\Tencent\QQNT\resources\app\package.json",
            r"D:/Tencent/QQNT/RESOURCES/APP/loadCinlan.js",
            r"D:\Tencent\QQNT\resources\app\application.asar\loadCinlan.js",
        ] {
            let value = wide_null(path);
            let expected = if path.ends_with("package.json") {
                r"\resources\app\package.json"
            } else if path.contains("application.asar") {
                r"\resources\app\application.asar\loadCinlan.js"
            } else {
                r"\resources\app\loadCinlan.js"
            };
            assert!(unsafe { wide_path_ends_with(value.as_ptr(), expected) });
        }
    }

    #[test]
    fn unrelated_package_metadata_does_not_match() {
        let value = wide_null(r"D:\workspace\plugin\package.json");
        assert!(!unsafe { wide_path_ends_with(value.as_ptr(), r"\resources\app\package.json") });
    }

    #[test]
    fn content_guard_signature_requires_the_expected_control_flow() {
        let mut signature = [0_u8; 25];
        for (index, value) in [
            (0, 0xE8),
            (5, 0xE8),
            (10, 0x84),
            (11, 0xC0),
            (12, 0x0F),
            (13, 0x85),
            (18, 0x48),
            (19, 0x8D),
            (20, 0x0D),
        ] {
            signature[index] = value;
        }
        assert!(content_guard_pattern(&signature));
        signature[13] = 0x84;
        assert!(!content_guard_pattern(&signature));
    }
}
