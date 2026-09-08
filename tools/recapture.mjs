// 把 tools/tap 的捕获文件转成仓库内的三份基准数据：
//   internal/upstream/node24_clienthello.bin  —— TLS 握手指纹模板（取 generate 请求所在连接的 hello）
//   internal/proxy/clitools.json              —— CLI 内置工具定义（generate body.params.tools，原样保序）
//   fingerprint.json                          —— 本机设备指纹上报体（.gitignore，勿提交）
//
// 完整流程见 README「重捕获指纹基准」。
// 用法: node tools/recapture.mjs captured.jsonl
import { appendFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const src = process.argv[2];
if (!src) {
  console.error("usage: node tools/recapture.mjs <captured.jsonl>");
  process.exit(1);
}

const lines = readFileSync(src, "utf8")
  .split("\n")
  .map((l) => l.trim())
  .filter(Boolean)
  .map((l) => JSON.parse(l));

const gen = [...lines].reverse().find((c) => c.http_request.startsWith("POST /alpha/generate"));
if (!gen) {
  console.error("捕获文件中没有 /alpha/generate 请求；请按 README 步骤完整跑一次 CLI");
  process.exit(1);
}
const body = JSON.parse(gen.http_body);

// 1) ClientHello 模板：完整 TLS 记录（0x16 开头）。
const hello = Buffer.from(gen.client_hello_hex, "hex");
if (hello[0] !== 0x16) {
  console.error("client_hello_hex 不是 TLS record");
  process.exit(1);
}
writeFileSync(join(root, "internal/upstream/node24_clienthello.bin"), hello);
console.log(`node24_clienthello.bin: ${hello.length} bytes`);

// 2) tools 定义（紧凑 JSON，键序保持捕获原样）。
const tools = JSON.stringify(body.params.tools);
writeFileSync(join(root, "internal/proxy/clitools.json"), tools);
console.log(`clitools.json: ${body.params.tools.length} tools, ${tools.length} bytes`);

// 3) 设备指纹上报体。
const fp = [...lines].reverse().find((c) => c.http_request.startsWith("POST /alpha/fingerprint/record"));
if (fp) {
  writeFileSync(join(root, "fingerprint.json"), fp.http_body);
  console.log("fingerprint.json: 已更新（勿提交）");
} else {
  console.warn("警告: 捕获文件中没有 fingerprint/record 请求，保留现有 fingerprint.json");
}

// 4) 顺带打印本次捕获的元数据，便于确认与真实 CLI 对齐。
const heads = gen.http_request.split("\r\n");
console.log("\ngenerate 请求头顺序:");
for (const h of heads.slice(0, heads.indexOf(""))) console.log("  " + (h.toLowerCase().startsWith("authorization") ? "Authorization: [redacted]" : h));
console.log("body 顶层键序:", Object.keys(body).join(","));
console.log("params 键序:", Object.keys(body.params).join(","));
console.log("x-project-slug:", heads.find((h) => h.startsWith("x-project-slug")));
console.log("threadId:", body.threadId);
