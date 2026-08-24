#!/usr/bin/env node
// WeTTY 2.5 behind HTTPS reverse proxy sees HTTP, so helmet CSP only
// allows ws:// while the browser uses wss://. Allow both and trust
// X-Forwarded-Proto.
const fs = require('fs');
const path = require('path');

const root = process.argv[2] || '/usr/local/lib/node_modules/wetty';
const secPath = path.join(root, 'build/server/socketServer/security.js');
const srvPath = path.join(root, 'build/server/socketServer.js');

if (!fs.existsSync(secPath) || !fs.existsSync(srvPath)) {
  process.stderr.write(`wetty files not found under ${root}\n`);
  process.exit(1);
}

let sec = fs.readFileSync(secPath, 'utf8');
const oldConnect = `(req.protocol === 'http' ? 'ws://' : 'wss://') + req.get('host')`;
const newConnect = `'ws://' + req.get('host'),\n                    'wss://' + req.get('host')`;
if (sec.includes(oldConnect)) {
  sec = sec.replace(oldConnect, newConnect);
  fs.writeFileSync(secPath, sec);
} else if (!sec.includes("'wss://' + req.get('host')")) {
  process.stderr.write('security.js: expected connectSrc snippet not found\n');
  process.exit(1);
}

let srv = fs.readFileSync(srvPath, 'utf8');
if (!srv.includes("app.set('trust proxy'")) {
  const needle = 'const app = express();\n';
  if (!srv.includes(needle)) {
    process.stderr.write('socketServer.js: express() snippet not found\n');
    process.exit(1);
  }
  srv = srv.replace(needle, `${needle}    app.set('trust proxy', 1);\n`);
  fs.writeFileSync(srvPath, srv);
}

process.stdout.write('patched wetty CSP + trust proxy\n');