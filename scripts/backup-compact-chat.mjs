// Read-only source snapshot for repeatable compact experiments. Never restores
// or modifies the source chat. Run with: node scripts/backup-compact-chat.mjs
// <chats-directory> <chat-id> <backup-parent-directory>
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs/promises';
import path from 'node:path';

const [sourceArg, chatId, parentArg] = process.argv.slice(2);
if (!sourceArg || !parentArg || !/^[a-zA-Z0-9_-]+$/.test(chatId || '')) throw new Error('Expected chats directory, safe chat ID, backup parent');
const source = path.resolve(sourceArg);
const parent = path.resolve(parentArg);
if (parent === source || parent.startsWith(source + path.sep)) throw new Error('Backup must be outside the source chats directory');
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
function metadata() {
  const read = table => JSON.parse(execFileSync('sqlite3', ['-readonly', '-json', path.join(source, 'chats.db'), `SELECT * FROM ${table} WHERE CHAT_ID_='${chatId}' ORDER BY ${table === 'RUNS' ? 'RUN_ID_' : 'CHAT_ID_'};`], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 }) || '[]');
  return { chat: read('CHATS'), runs: read('RUNS') };
}
const meta = metadata();
if (meta.chat.length !== 1) throw new Error('Chat metadata not found');
const filename = `${chatId}.jsonl`;
const before = hash(await fs.readFile(path.join(source, filename)));
const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..*$/, 'Z');
const destination = path.join(parent, chatId, stamp);
await fs.mkdir(path.dirname(destination), { recursive: true });
await fs.mkdir(destination); // Refuse timestamp collisions, never overwrite.
await fs.copyFile(path.join(source, filename), path.join(destination, filename));
const resources = path.join(source, chatId);
try { await fs.cp(resources, path.join(destination, 'resources'), { recursive: true, dereference: false, errorOnExist: true, force: false }); }
catch (error) { if (error.code !== 'ENOENT') throw error; }
await fs.writeFile(path.join(destination, 'metadata.json'), JSON.stringify(meta, null, 2) + '\n', { flag: 'wx' });
const entries = [];
async function inspect(directory, prefix = '') {
  for (const entry of (await fs.readdir(directory, { withFileTypes: true })).sort((a,b) => a.name.localeCompare(b.name))) {
    const relative = path.posix.join(prefix, entry.name);
    const absolute = path.join(directory, entry.name);
    if (entry.isDirectory()) await inspect(absolute, relative);
    else if (entry.isSymbolicLink()) { const target = await fs.readlink(absolute); entries.push({ path: relative, type: 'symlink', target, sha256: hash(target) }); }
    else { const bytes = await fs.readFile(absolute); entries.push({ path: relative, size: bytes.length, sha256: hash(bytes) }); }
  }
}
await inspect(destination);
for (const entry of entries) {
  if (entry.type === 'symlink' || entry.path === 'metadata.json') continue;
  const original = entry.path === filename ? path.join(source, filename) : path.join(resources, entry.path.slice('resources/'.length));
  if (hash(await fs.readFile(original)) !== entry.sha256) throw new Error('Source changed during backup: ' + entry.path);
}
if (before !== hash(await fs.readFile(path.join(source, filename))) || JSON.stringify(meta) !== JSON.stringify(metadata())) throw new Error('Source changed; backup is incomplete');
await fs.writeFile(path.join(destination, 'SHA256SUMS'), entries.map(entry => `${entry.sha256}  ${entry.path}${entry.type === 'symlink' ? ' (symlink target)' : ''}`).join('\n') + '\n', { flag: 'wx' });
await fs.writeFile(path.join(destination, 'manifest.json'), JSON.stringify({ chatId, source, createdAt: new Date().toISOString(), jsonlSha256: before, files: entries, verified: true }, null, 2) + '\n', { flag: 'wx' });
console.log(JSON.stringify({ destination, verified: true, files: entries.length, jsonlSha256: before }));
