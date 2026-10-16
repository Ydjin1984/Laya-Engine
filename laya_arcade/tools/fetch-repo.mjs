// fetch-repo.mjs — скачивает содержимое референсного репозитория через GitHub API.
// Node использует OpenSSL, поэтому TLS работает там, где системные curl/git падают.
import fs from 'node:fs';
import path from 'node:path';

const REPO = process.argv[2] || 'kevinbadi/laya-decision-brain';
const OUT = process.argv[3] || path.join(process.cwd(), 'reference', 'laya-decision-brain');
const MAX_FILES = 300;
const MAX_BYTES = 400 * 1024;
const TEXT_EXT = new Set(['.md', '.py', '.js', '.mjs', '.cjs', '.ts', '.tsx', '.json', '.html', '.css', '.txt', '.yml', '.yaml', '.toml', '.ipynb', '.sh', '.bat', '.cmd', '.sql', '.env', '.example']);

const api = (p) => `https://api.github.com/${p}`;
const headers = { 'user-agent': 'laya-arcade-research', accept: 'application/vnd.github+json' };

async function getJson(url) {
  const r = await fetch(url, { headers });
  if (!r.ok) throw new Error(`HTTP ${r.status} ${url}`);
  return r.json();
}

const repo = await getJson(api(`repos/${REPO}`));
console.log(`репозиторий: ${repo.full_name}`);
console.log(`описание: ${repo.description}`);
console.log(`ветка по умолчанию: ${repo.default_branch}, размер: ${Math.round((repo.size || 0) / 1024)} МБ, обновлён: ${repo.updated_at}`);

const tree = await getJson(api(`repos/${REPO}/git/trees/${repo.default_branch}?recursive=1`));
const blobs = (tree.tree || []).filter((t) => t.type === 'blob');
console.log(`файлов в дереве: ${blobs.length}`);

fs.mkdirSync(OUT, { recursive: true });

let saved = 0, skipped = 0;
const listing = [];
for (const b of blobs) {
  const ext = path.extname(b.path).toLowerCase();
  const isText = TEXT_EXT.has(ext) || path.basename(b.path).toLowerCase().startsWith('readme') || !ext;
  if (!isText || b.size > MAX_BYTES || saved >= MAX_FILES) { skipped++; continue; }
  const url = `https://raw.githubusercontent.com/${REPO}/${repo.default_branch}/${encodeURI(b.path)}`;
  try {
    const r = await fetch(url, { headers: { 'user-agent': 'laya-arcade-research' } });
    if (!r.ok) { skipped++; continue; }
    const text = await r.text();
    const dest = path.join(OUT, b.path);
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.writeFileSync(dest, text, 'utf8');
    saved++;
    listing.push({ path: b.path, size: b.size });
  } catch (e) {
    skipped++;
  }
}

listing.sort((a, b) => a.path.localeCompare(b.path));
fs.writeFileSync(path.join(OUT, '_file-list.json'), JSON.stringify({ repo: repo.full_name, description: repo.description, defaultBranch: repo.default_branch, files: listing }, null, 1), 'utf8');

console.log(`\nскачано файлов: ${saved}, пропущено: ${skipped}`);
console.log(`каталог: ${OUT}\n`);
for (const f of listing) console.log(`  ${String(f.size).padStart(7)}  ${f.path}`);
