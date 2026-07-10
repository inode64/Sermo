import { createServer } from "node:http";
import { readFile } from "node:fs/promises";

const host = "127.0.0.1";
const port = 4173;
const page = (await readFile(new URL("../../internal/web/index.html", import.meta.url), "utf8"))
  .replaceAll("{{CSP_NONCE}}", "playwright")
  .replaceAll("{{VERSION}}", "test");

createServer((request, response) => {
  if (request.url === "/" || request.url === "/index.html") {
    response.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" });
    response.end(page);
    return;
  }
  response.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" });
  response.end("not found");
}).listen(port, host, () => {
  process.stdout.write(`web fixture listening on http://${host}:${port}\n`);
});
