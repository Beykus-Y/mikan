// README screenshots: a demo panel with its node in Docker (compose.yaml), demo users and
// a month of traffic, the main pages in English and Russian, on a desktop and a phone.
//
//   cd scripts/screens && npm install
//   MIKAN_IMAGE=mikan:0.3.8 CHROME=/path/to/chrome node shoot.mjs
//
// Writes .github/assets/screens/{en,ru}/*.webp and deletes the demo stack afterwards.
import puppeteer from "puppeteer-core";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import crypto from "node:crypto";
import path from "node:path";
import fs from "node:fs";

const CHROME = process.env.CHROME;
if (!CHROME) throw new Error("set CHROME to a Chrome or chrome-headless-shell binary");
const dir = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(dir, "../../.github/assets/screens");
const ADMIN = "demo-admin-path-000000";
const SUB = "demosubs0000";
const BASE = `https://127.0.0.1:2090/${ADMIN}/`;
// The demo stack lives for a minute on 127.0.0.1; its admin password is thrown away.
const PASS = crypto.randomBytes(18).toString("base64url");
const env = { ...process.env, MIKAN_IMAGE: process.env.MIKAN_IMAGE || "mikan:dev", MIKAN_TEST_POSTGRES_PASSWORD: crypto.randomBytes(32).toString("hex") };
process.env.NODE_TLS_REJECT_UNAUTHORIZED = "0"; // the demo panel's certificate is self-signed
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const compose = (args, input) =>
  execFileSync("docker", ["compose", "-f", path.join(dir, "compose.yaml"), ...args], { env, input, stdio: [input ? "pipe" : "ignore", "ignore", "inherit"] });

// Writes straight into the panel's database while the panel is stopped.
function sql(text) {
  compose(["stop", "panel"]);
  compose(["exec", "-T", "postgres", "sh", "-c", 'PGPASSWORD="$POSTGRES_PASSWORD" exec psql -h /run/postgresql -U mikan -d mikan -v ON_ERROR_STOP=1'], text);
  compose(["start", "panel"]);
}

async function ready() {
  for (let i = 0; i < 60; i++) {
    try {
      if ((await fetch(BASE)).status === 200) return;
    } catch {
      // not up yet
    }
    await sleep(1000);
  }
  throw new Error("the demo panel did not start");
}

const PEOPLE = [
  ["Alex Morgan", "@alexm", ["friends"]],
  ["Maria Lopez", "@maria", ["family"]],
  ["Ivan Petrov", "@ivanp", []],
  ["Sofia Rossi", "@sofia", ["friends"]],
  ["Daniel Kim", "@dkim", []],
  ["Olga Smirnova", "@olga", ["family"]],
  ["Nikita Orlov", "@nikita", []],
  ["Emma Weber", "@emma", ["work"]],
  ["Leo Martin", "@leo", []],
  ["Anna Novak", "@anna", ["friends"]],
  ["Mark Chen", "@markc", ["work"]],
  ["Eva Fischer", "@eva", []],
  ["Tom Becker", "@tomb", []],
  ["Yuki Tanaka", "@yuki", ["work"]],
  ["Lucas Silva", "@lucas", []],
  ["Nina Kovac", "@nina", ["family"]],
  ["Omar Haddad", "@omar", []],
  ["Kate Wilson", "@kate", ["friends"]],
];

const NAMES = {
  en: { tariffs: ["Trial", "Standard", "Unlimited"], node: "🇳🇱 Netherlands" },
  ru: { tariffs: ["Пробный", "Стандарт", "Безлимит"], node: "🇳🇱 Нидерланды" },
};

// A month of traffic with an evening peak, and the users' states: a few expire soon, one
// ran out of traffic, one is switched off.
function seed(ids, now) {
  const GB = 1024 ** 3;
  const rnd = (i, k) => {
    const x = Math.sin(i * 12.9898 + k * 78.233) * 43758.5453;
    return x - Math.floor(x);
  };
  const lines = ["BEGIN;"];
  const hour = Math.floor(now / 3600), day = Math.floor(now / 86400);
  ids.forEach((id, i) => {
    const weight = 0.4 + rnd(i, 1) * 1.6;
    let used = 0;
    for (let h = hour - 30; h <= hour; h++) {
      const local = (((h + 3) % 24) + 24) % 24; // Moscow time
      const curve = 0.25 + Math.max(0, Math.sin(((local - 9) / 24) * Math.PI * 2)) * 0.9 + (local >= 19 && local <= 23 ? 0.6 : 0);
      const down = Math.round(weight * curve * (0.12 + rnd(i, h) * 0.2) * GB);
      lines.push(`INSERT INTO traffic_hourly (user_id, hour, up, down) VALUES (${id}, ${h}, ${Math.round(down * 0.09)}, ${down}) ON CONFLICT(user_id,hour) DO UPDATE SET up=excluded.up,down=excluded.down;`);
    }
    for (let d = day - 34; d <= day; d++) {
      const weekend = [5, 6].includes(new Date(d * 86400e3).getUTCDay()) ? 1.35 : 1;
      const down = Math.round(weight * weekend * (1.6 + rnd(i, d) * 2.4) * GB);
      if (d > day - 20) used += down;
      lines.push(`INSERT INTO traffic_daily (user_id, day, up, down) VALUES (${id}, ${d}, ${Math.round(down * 0.09)}, ${down}) ON CONFLICT(user_id,day) DO UPDATE SET up=excluded.up,down=excluded.down;`);
    }
    const expires = i % 7 === 3 ? now + 2 * 86400 : i % 9 === 5 ? now + 5 * 86400 : now + (12 + Math.round(rnd(i, 9) * 18)) * 86400;
    lines.push(`UPDATE users SET used_down = ${used}, used_up = ${Math.round(used * 0.09)}, total_down = ${used * 3}, total_up = ${Math.round(used * 0.27)}, expires_at = ${expires} WHERE id = ${id};`);
  });
  lines.push(`UPDATE users SET status = 'disabled' WHERE id = ${ids[12]};`);
  lines.push(`UPDATE users SET used_down = traffic_limit WHERE id = ${ids[4]} AND traffic_limit IS NOT NULL;`);
  lines.push(`INSERT INTO settings (key, value) VALUES ('brand', '"mikan"') ON CONFLICT(key) DO UPDATE SET value=excluded.value;`);
  lines.push("COMMIT;");
  return lines.join("\n");
}

function names(lang) {
  const n = NAMES[lang];
  return [
    `UPDATE tariffs SET name = '${n.tariffs[0]}' WHERE sort = 1;`,
    `UPDATE tariffs SET name = '${n.tariffs[1]}' WHERE sort = 2;`,
    `UPDATE tariffs SET name = '${n.tariffs[2]}' WHERE sort = 3;`,
    `UPDATE nodes SET name = '${n.node}' WHERE id = 1;`,
  ].join("\n");
}

async function shoot(browser, lang, subURL) {
  const dest = path.join(out, lang);
  fs.mkdirSync(dest, { recursive: true });
  const desktop = await browser.newPage();
  await desktop.setViewport({ width: 1440, height: 900, deviceScaleFactor: 1.5 });
  await desktop.goto(BASE, { waitUntil: "networkidle0" });
  await desktop.evaluate((l) => localStorage.setItem("mikan.lang", l), lang);
  for (const [name, route] of [["dashboard", ""], ["users", "users"], ["inbounds", "inbounds"], ["telegram", "telegram"], ["settings", "settings"]]) {
    await desktop.goto(BASE + route, { waitUntil: "networkidle0" });
    await sleep(1800); // the cards rise and the chart draws
    await desktop.screenshot({ path: path.join(dest, `${name}.webp`), type: "webp", quality: 82 });
  }
  const phone = await browser.newPage();
  await phone.setViewport({ width: 390, height: 844, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  for (const [name, url] of [["phone-dashboard", BASE], ["phone-subscription", subURL]]) {
    await phone.goto(url, { waitUntil: "networkidle0" });
    await phone.evaluate((l) => localStorage.setItem("mikan.lang", l), lang);
    await phone.goto(url, { waitUntil: "networkidle0" });
    await sleep(1800);
    await phone.screenshot({ path: path.join(dest, `${name}.webp`), type: "webp", quality: 82 });
  }
  await desktop.close();
  await phone.close();
}

compose(["down", "-v", "--remove-orphans"]);
compose(["up", "-d", "--wait", "postgres"]);
compose(["run", "--rm", "--no-deps", "-T", "panel", "admin", "bootstrap", "--public-host", "vpn.example.com", "--port", "2053", "--admin-path", ADMIN, "--sub-path", SUB, "--username", "admin", "--password-stdin"], PASS + "\n");
compose(["up", "-d"]);
try {
  await ready();
  const browser = await puppeteer.launch({ executablePath: CHROME, headless: "shell", acceptInsecureCerts: true, args: ["--hide-scrollbars", "--force-color-profile=srgb"] });
  const page = await browser.newPage();
  await page.goto(BASE + "login", { waitUntil: "networkidle0" });
  await page.type("#login-user", "admin");
  await page.type("#login-pass", PASS);
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => !location.pathname.endsWith("/login"), { timeout: 15000 });
  const users = await page.evaluate(async (people) => {
    const me = await (await fetch("api/v1/auth/me")).json();
    const headers = { "Content-Type": "application/json", "X-CSRF-Token": me.csrf_token };
    const tariffs = await (await fetch("api/v1/tariffs")).json();
    const pick = (i) => tariffs[i % 5 === 0 ? 2 : i % 6 === 1 ? 0 : 1].id;
    for (const [i, [name, contact, tags]] of people.entries()) {
      const r = await fetch("api/v1/users", { method: "POST", headers, body: JSON.stringify({ name, contact, tags, tariff_id: pick(i) }) });
      if (!r.ok) throw new Error(`user ${name}: ${r.status} ${await r.text()}`);
    }
    return (await (await fetch("api/v1/users?limit=100")).json()).items.map((u) => ({ id: u.id, sub: u.sub_url }));
  }, PEOPLE);
  await page.close();
  const now = Math.floor(Date.now() / 1000);
  const subURL = users[1].sub.replace(/^https:\/\/[^/]+/, "https://127.0.0.1:2090");
  for (const lang of ["en", "ru"]) {
    sql((lang === "en" ? seed(users.map((u) => u.id), now) + "\n" : "") + names(lang));
    await ready();
    await shoot(browser, lang, subURL);
    console.log("screens:", lang);
  }
  await browser.close();
} finally {
  compose(["down", "-v", "--remove-orphans"]);
}
