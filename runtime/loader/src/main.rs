#![cfg(windows)]
#![allow(non_snake_case)]

use std::env;
use std::ffi::{c_char, c_void};
use std::io;
use std::mem::{self, MaybeUninit};
use std::path::{Path, PathBuf};
use std::ptr::null_mut;
use std::time::{Duration, Instant};

type Bool = i32;
type Dword = u32;
type Handle = *mut c_void;
type Hmodule = *mut c_void;

const CREATE_SUSPENDED: Dword = 0x0000_0004;
const MEM_COMMIT: Dword = 0x0000_1000;
const MEM_RESERVE: Dword = 0x0000_2000;
const MEM_RELEASE: Dword = 0x0000_8000;
const PAGE_READWRITE: Dword = 0x04;
const INFINITE: Dword = 0xffff_ffff;

#[repr(C)]
struct StartupInfoW {
    cb: Dword,
    reserved: *mut u16,
    desktop: *mut u16,
    title: *mut u16,
    x: Dword,
    y: Dword,
    x_size: Dword,
    y_size: Dword,
    x_count_chars: Dword,
    y_count_chars: Dword,
    fill_attribute: Dword,
    flags: Dword,
    show_window: u16,
    reserved2_size: u16,
    reserved2: *mut u8,
    std_input: Handle,
    std_output: Handle,
    std_error: Handle,
}

#[repr(C)]
struct ProcessInformation {
    process: Handle,
    thread: Handle,
    process_id: Dword,
    thread_id: Dword,
}

type ThreadStart = unsafe extern "system" fn(*mut c_void) -> Dword;

#[link(name = "kernel32")]
extern "system" {
    fn CloseHandle(handle: Handle) -> Bool;
    fn CreateProcessW(
        application_name: *const u16,
        command_line: *mut u16,
        process_attributes: *mut c_void,
        thread_attributes: *mut c_void,
        inherit_handles: Bool,
        creation_flags: Dword,
        environment: *mut c_void,
        current_directory: *const u16,
        startup_info: *mut StartupInfoW,
        process_information: *mut ProcessInformation,
    ) -> Bool;
    fn CreateRemoteThread(
        process: Handle,
        thread_attributes: *mut c_void,
        stack_size: usize,
        start_address: Option<ThreadStart>,
        parameter: *mut c_void,
        creation_flags: Dword,
        thread_id: *mut Dword,
    ) -> Handle;
    fn GetExitCodeThread(thread: Handle, exit_code: *mut Dword) -> Bool;
    fn GetModuleHandleW(name: *const u16) -> Hmodule;
    fn GetProcAddress(module: Hmodule, name: *const c_char) -> *mut c_void;
    fn ResumeThread(thread: Handle) -> Dword;
    fn TerminateProcess(process: Handle, exit_code: u32) -> Bool;
    fn VirtualAllocEx(
        process: Handle,
        address: *mut c_void,
        size: usize,
        allocation_type: Dword,
        protection: Dword,
    ) -> *mut c_void;
    fn VirtualFreeEx(process: Handle, address: *mut c_void, size: usize, free_type: Dword) -> Bool;
    fn WaitForSingleObject(handle: Handle, milliseconds: Dword) -> Dword;
    fn WriteProcessMemory(
        process: Handle,
        base_address: *mut c_void,
        buffer: *const c_void,
        size: usize,
        written: *mut usize,
    ) -> Bool;
}

fn main() {
    if let Err(error) = run() {
        eprintln!("cinlan QQNT loader failed: {error}");
        std::process::exit(1);
    }
}

fn run() -> io::Result<()> {
    let mut args = env::args_os().skip(1);
    let qq_path = args.next().map(PathBuf::from).ok_or_else(|| {
        invalid("usage: cinlan-qq-loader.exe <QQ.exe> <hook.dll> [QQ arguments...]")
    })?;
    let hook_path = args
        .next()
        .map(PathBuf::from)
        .ok_or_else(|| invalid("hook DLL path is required"))?;
    let qq_args: Vec<String> = args
        .map(|value| value.to_string_lossy().into_owned())
        .collect();

    require_file(&qq_path, "QQ executable")?;
    require_file(&hook_path, "Cinlan hook DLL")?;
    for key in [
        "CINLAN_QQNT_PATCH_PACKAGE",
        "CINLAN_QQNT_LOAD_PATH",
        "CINLAN_QQNT_RUNTIME_PATH",
        "CINLAN_QQNT_WRAPPER_PATH",
        "CINLAN_QQNT_IPC_ADDR",
        "CINLAN_QQNT_IPC_TOKEN",
        "CINLAN_QQNT_HOOK_STATUS_PATH",
    ] {
        if env::var_os(key).is_none() {
            return Err(invalid(&format!("{key} is required")));
        }
    }

    let qq_path = qq_path.canonicalize()?;
    let hook_path = hook_path.canonicalize()?;
    let current_directory = qq_path
        .parent()
        .ok_or_else(|| invalid("QQ executable has no parent directory"))?;

    let mut command = quote_windows_argument(&qq_path.to_string_lossy());
    for argument in qq_args {
        command.push(' ');
        command.push_str(&quote_windows_argument(&argument));
    }
    let mut command_wide = wide_null(&command);
    let application_wide = wide_null(qq_path.as_os_str().to_string_lossy().as_ref());
    let directory_wide = wide_null(current_directory.as_os_str().to_string_lossy().as_ref());

    let mut startup: StartupInfoW = unsafe { mem::zeroed() };
    startup.cb = mem::size_of::<StartupInfoW>() as Dword;
    let mut process = MaybeUninit::<ProcessInformation>::uninit();
    let created = unsafe {
        CreateProcessW(
            application_wide.as_ptr(),
            command_wide.as_mut_ptr(),
            null_mut(),
            null_mut(),
            0,
            CREATE_SUSPENDED,
            null_mut(),
            directory_wide.as_ptr(),
            &mut startup,
            process.as_mut_ptr(),
        )
    };
    if created == 0 {
        return Err(io::Error::last_os_error());
    }
    let process = unsafe { process.assume_init() };

    let launch_result = inject_library(process.process, &hook_path)
        .map_err(|error| {
            let status = env::var_os("CINLAN_QQNT_HOOK_STATUS_PATH")
                .and_then(|path| std::fs::read_to_string(path).ok());
            match status {
                Some(status) if status.starts_with("error:") => invalid(&status),
                _ => error,
            }
        })
        .and_then(|_| {
            let result = unsafe { ResumeThread(process.thread) };
            if result == u32::MAX {
                Err(io::Error::last_os_error())
            } else {
                wait_hook_status()
            }
        });

    if launch_result.is_err() {
        unsafe {
            TerminateProcess(process.process, 1);
        }
    }
    unsafe {
        CloseHandle(process.thread);
        CloseHandle(process.process);
    }
    launch_result?;

    println!("cinlan QQNT runtime launched, pid={}", process.process_id);
    Ok(())
}

fn wait_hook_status() -> io::Result<()> {
    let path = env::var_os("CINLAN_QQNT_HOOK_STATUS_PATH")
        .ok_or_else(|| invalid("hook status path missing"))?;
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        if let Ok(status) = std::fs::read_to_string(&path) {
            if status == "ready" {
                return Ok(());
            }
            if status.starts_with("error:") {
                return Err(invalid(&status));
            }
        }
        if Instant::now() >= deadline {
            return Err(invalid(
                "QQNT hook status timeout; refusing unverified startup",
            ));
        }
        std::thread::sleep(Duration::from_millis(50));
    }
}

fn inject_library(process: Handle, hook_path: &Path) -> io::Result<()> {
    let hook_wide = wide_null(hook_path.as_os_str().to_string_lossy().as_ref());
    let byte_count = hook_wide.len() * mem::size_of::<u16>();
    let remote = unsafe {
        VirtualAllocEx(
            process,
            null_mut(),
            byte_count,
            MEM_COMMIT | MEM_RESERVE,
            PAGE_READWRITE,
        )
    };
    if remote.is_null() {
        return Err(io::Error::last_os_error());
    }

    let result = (|| {
        let mut written = 0usize;
        if unsafe {
            WriteProcessMemory(
                process,
                remote,
                hook_wide.as_ptr().cast(),
                byte_count,
                &mut written,
            )
        } == 0
            || written != byte_count
        {
            return Err(io::Error::last_os_error());
        }

        let kernel32_name = wide_null("kernel32.dll");
        let kernel32 = unsafe { GetModuleHandleW(kernel32_name.as_ptr()) };
        if kernel32.is_null() {
            return Err(io::Error::last_os_error());
        }
        let load_library = unsafe { GetProcAddress(kernel32, c"LoadLibraryW".as_ptr()) };
        if load_library.is_null() {
            return Err(io::Error::last_os_error());
        }
        let start: ThreadStart = unsafe { mem::transmute(load_library) };
        let thread = unsafe {
            CreateRemoteThread(process, null_mut(), 0, Some(start), remote, 0, null_mut())
        };
        if thread.is_null() {
            return Err(io::Error::last_os_error());
        }

        let wait_result = unsafe { WaitForSingleObject(thread, INFINITE) };
        let mut exit_code = 0;
        let exit_result = unsafe { GetExitCodeThread(thread, &mut exit_code) };
        unsafe {
            CloseHandle(thread);
        }
        if wait_result != 0 || exit_result == 0 || exit_code == 0 {
            return Err(invalid("remote LoadLibraryW failed"));
        }
        Ok(())
    })();

    unsafe {
        VirtualFreeEx(process, remote, 0, MEM_RELEASE);
    }
    result
}

fn quote_windows_argument(value: &str) -> String {
    if !value.is_empty()
        && !value
            .bytes()
            .any(|byte| byte == b' ' || byte == b'\t' || byte == b'"')
    {
        return value.to_owned();
    }

    let mut output = String::from("\"");
    let mut backslashes = 0usize;
    for ch in value.chars() {
        match ch {
            '\\' => backslashes += 1,
            '"' => {
                output.push_str(&"\\".repeat(backslashes * 2 + 1));
                output.push('"');
                backslashes = 0;
            }
            _ => {
                output.push_str(&"\\".repeat(backslashes));
                backslashes = 0;
                output.push(ch);
            }
        }
    }
    output.push_str(&"\\".repeat(backslashes * 2));
    output.push('"');
    output
}

fn wide_null(value: &str) -> Vec<u16> {
    value.encode_utf16().chain(Some(0)).collect()
}

fn require_file(path: &Path, label: &str) -> io::Result<()> {
    if path.is_file() {
        Ok(())
    } else {
        Err(invalid(&format!(
            "{label} does not exist: {}",
            path.display()
        )))
    }
}

fn invalid(message: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, message)
}

#[cfg(test)]
mod tests {
    #[test]
    fn windows_argument_quotes_preserve_spaces_and_trailing_backslash() {
        assert_eq!(super::quote_windows_argument("QQ.exe"), "QQ.exe");
        assert_eq!(
            super::quote_windows_argument("D:\\QQ dir\\"),
            "\"D:\\QQ dir\\\\\""
        );
    }
}
