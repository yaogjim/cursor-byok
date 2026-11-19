import { readFileSync } from "node:fs";

const css = readFileSync(new URL("../src/style/global.css", import.meta.url), "utf8");
function tokens(selector) {
  const block = css.match(new RegExp(`${selector}\\s*\\{([^}]+)\\}`));
  if (!block) throw new Error(`找不到主题：${selector}`);
  return Object.fromEntries([...block[1].matchAll(/(--[\w-]+):\s*(#[\da-f]{6})\s*;/gi)].map((m) => [m[1], m[2]]));
}
function luminance(hex) {
  if (!/^#[\da-f]{6}$/i.test(hex || "")) throw new Error(`缺失或不支持的颜色：${hex}`);
  const rgb = hex.slice(1).match(/../g).map((v) => parseInt(v, 16) / 255)
    .map((v) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
}
let failures = 0;
function check(theme, name, foreground, background) {
  if (!foreground || !background) {
    console.log(`FAIL ${theme} ${name}: 缺失 token`);
    failures++;
    return;
  }
  const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
  const ratio = (values[0] + 0.05) / (values[1] + 0.05);
  const pass = ratio >= 4.5;
  console.log(`${pass ? "PASS" : "FAIL"} ${theme} ${name}: ${ratio.toFixed(3)}:1 (${foreground} / ${background})`);
  if (!pass) failures++;
}
const light = tokens(":root");
const dark = { ...light, ...tokens(':root\\[data-theme="dark"\\]') };
for (const [theme, colors] of [["light", light], ["dark", dark]]) {
  for (const foreground of ["text-faint", "text-muted", "text-secondary", "text"]) {
    for (const background of ["app-bg", "surface", "surface-muted", "card-foot"]) {
      check(theme, `${foreground} / ${background}`, colors[`--color-${foreground}`], colors[`--color-${background}`]);
    }
  }
  for (const background of ["primary-strong", "primary-strong-hover"]) {
    check(theme, `white / ${background}`, "#ffffff", colors[`--color-${background}`]);
  }
}
console.log(`对比度检查：${failures === 0 ? "PASS" : `FAIL (${failures})`}`);
process.exitCode = failures ? 1 : 0;
