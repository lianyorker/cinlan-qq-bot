'use strict';

// Read-only inspection of official PE binding strings and their RIP-relative
// references. No third-party runtime implementation or official file writes.
const fs = require('node:fs');
const file = process.argv[2];
if (!file) throw new Error('usage: node inspect-qqnt-bindings.cjs <wrapper.node>');
const b = fs.readFileSync(file);
const nt = b.readUInt32LE(0x3c);
const opt = nt + 24;
const sections = [];
for (let i = 0; i < b.readUInt16LE(nt + 6); i++) {
  const p = opt + b.readUInt16LE(nt + 20) + i * 40;
  sections.push({ name: b.subarray(p, p + 8).toString().replace(/\0/g, ''),
    rva: b.readUInt32LE(p + 12), size: b.readUInt32LE(p + 16), raw: b.readUInt32LE(p + 20) });
}
function raw(rva) { const s = sections.find(s => rva >= s.rva && rva < s.rva + s.size); return s ? rva - s.rva + s.raw : -1; }
function str(rva) { const p = raw(rva); if (p < 0) return ''; const e = b.indexOf(0, p); if (e < p || e - p > 500) return ''; const t = b.subarray(p, e).toString('latin1'); return /^[ -~]+$/.test(t) ? t : ''; }
const pdata = sections.find(s => s.name === '.pdata');
const functions = [];
for (let p = pdata.raw; p + 12 <= pdata.raw + pdata.size; p += 12) functions.push([b.readUInt32LE(p), b.readUInt32LE(p + 4)]);
const text = sections.find(s => s.name === '.text');
const refs = [];
for (let p = text.raw; p < text.raw + text.size - 7; p++) {
  if ((b[p] === 0x48 || b[p] === 0x4c) && b[p + 1] === 0x8d && (b[p + 2] & 0xc7) === 5) {
    const rva = text.rva + p - text.raw; const value = str(rva + 7 + b.readInt32LE(p + 3));
    if (value) refs.push({ rva, value });
  }
}
for (const name of process.argv.slice(3)) {
  const matches = refs.filter(r => r.value.includes(name));
  console.log('=== ' + name);
  for (const m of matches) {
    const f = functions.find(f => m.rva >= f[0] && m.rva < f[1]);
    if (!f) continue;
    console.log(JSON.stringify({ function: f.map(x => '0x' + x.toString(16)), refs: refs.filter(r => r.rva >= f[0] && r.rva < f[1]) }));
    if (process.env.QQNT_INSPECT_CALLS === '1') {
      for (let r = f[0]; r < f[1] - 5; r++) {
        const p = raw(r); if (b[p] !== 0xe8) continue;
        const t = r + 5 + b.readInt32LE(p + 1);
        const c = functions.find(c => t === c[0]);
        if (c) console.log(JSON.stringify({ call: c.map(x => '0x' + x.toString(16)), refs: refs.filter(x => x.rva >= c[0] && x.rva < c[1]) }));
      }
    }
  }
}
