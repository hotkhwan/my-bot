const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(
  path.join(__dirname, "..", "internal", "dashboard", "dist", "index.html"),
  "utf8",
);
const start = html.lastIndexOf("<script>");
const end = html.indexOf("</script>", start);
if (start < 0 || end < 0) throw new Error("inline dashboard script not found");
new Function(html.slice(start + "<script>".length, end));

const ids = new Set(Array.from(html.matchAll(/id="([^"]+)"/g), (m) => m[1]));
const refs = Array.from(html.matchAll(/\$\("([^"]+)"\)/g), (m) => m[1]);
const missing = [...new Set(refs.filter((id) => !ids.has(id)))];
if (missing.length) throw new Error(`missing dashboard ids: ${missing.join(", ")}`);

console.log("dashboard JavaScript and element IDs OK");
