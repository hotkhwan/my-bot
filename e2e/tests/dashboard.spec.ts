import { test, expect, Page } from "@playwright/test";
import * as crypto from "crypto";

const BOT_TOKEN = "123:abc";
const TG_ID = 12345;

// Each password-flow test uses the seeded user from cmd/e2eserver. The login
// path is independent of Telegram and is enough for paper/backtest surfaces.
async function registerAndLogin(page: Page) {
  await page.goto("/");
  await page.fill("#username", "e2e_user");
  await page.fill("#password", "password123");
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/login")),
    page.click("#login"),
  ]);
  await expect(page.locator("#nav")).toBeVisible();
  await expect(page.locator("#view-home")).toBeVisible();
}

// Open the Trade tab, where the plan form, mission review, and Ask ANNY console live.
async function gotoTrade(page: Page) {
  await page.click('#nav button[data-view="orders"]');
  await expect(page.locator("#view-orders")).toBeVisible();
}

function signedTelegramUser() {
  const fields: Record<string, string> = {
    id: String(TG_ID),
    first_name: "E2E",
    username: "e2e_tg",
    auth_date: String(Math.floor(Date.now() / 1000)),
  };
  const dataCheck = Object.keys(fields)
    .sort()
    .map((key) => `${key}=${fields[key]}`)
    .join("\n");
  const secret = crypto.createHash("sha256").update(BOT_TOKEN).digest();
  fields.hash = crypto.createHmac("sha256", secret).update(dataCheck).digest("hex");
  return {
    id: Number(fields.id),
    first_name: fields.first_name,
    username: fields.username,
    auth_date: Number(fields.auth_date),
    hash: fields.hash,
  };
}

async function telegramToken(page: Page) {
  const res = await page.request.post("/api/telegram-login", { data: signedTelegramUser() });
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  expect(body.token).toBeTruthy();
  return String(body.token);
}

async function openWithToken(page: Page, token: string, path = "/orders") {
  await page.goto("/");
  await page.evaluate((t) => localStorage.setItem("token", t), token);
  await page.goto(path);
  await expect(page.locator("#nav")).toBeVisible();
  await expect(page.locator("#view-orders")).toBeVisible();
}

async function telegramLogin(page: Page) {
  const token = await telegramToken(page);
  await openWithToken(page, token);
  return token;
}

async function addTestnetKey(page: Page, token: string) {
  const res = await page.request.post("/api/credentials", {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      profile: "testnet",
      api_key: "e2e-testnet-key",
      api_secret: "e2e-testnet-secret",
      testnet: true,
    },
  });
  expect(res.ok()).toBeTruthy();
}

async function assessRiskPlan(page: Page, opts: {
  capital?: string;
  risk?: string;
  leverage?: string;
  symbol?: string;
  strategy?: string;
  duration?: string;
  ai?: boolean;
} = {}) {
  await page.fill("#g-capital", opts.capital ?? "100");
  await page.fill("#g-risk", opts.risk ?? "70");
  await page.fill("#g-leverage", opts.leverage ?? "25");
  await page.selectOption("#g-symbol", opts.symbol ?? "BTC");
  await page.selectOption("#g-strategy", opts.strategy ?? "ema");
  await page.selectOption("#g-duration", opts.duration ?? "1h");
  if (opts.ai ?? true) {
    await page.check("#g-ai");
  } else {
    await page.uncheck("#g-ai");
  }
  await page.click("#g-run");
  await expect(page.locator("#g-card")).toBeVisible({ timeout: 20_000 });
}

test("login -> risk-budget paper run shows real stats without Target field", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await expect(page.locator("#g-profit")).toHaveCount(0);
  await expect(page.locator("#view-orders")).not.toContainText("Target (USDT)");
  await expect(page.locator("#g-duration")).toHaveValue("1h");

  await assessRiskPlan(page);

  const card = page.locator("#g-card");
  await expect(card).toContainText("Simulated P/L · this window");
  await expect(card).toContainText(/Positive result in this window|Ran the full window/);
  await expect(card).not.toContainText("Target reached");
  await expect(page.locator("#bc-stats")).toContainText("Capital risk");
  await expect(page.locator("#bc-stats")).toContainText("Leverage use");
  await expect(page.locator("#bc-spark svg")).toBeVisible();
  await expect(page.locator("#g-history")).toContainText("BTCUSDT");
});

test("ANNY Basic no-setup asks for plan edit, not a zero paper result", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await assessRiskPlan(page, { risk: "60", strategy: "anny_basic", duration: "15m" });

  const card = page.locator("#g-card");
  await expect(page.locator("#g-card-title")).toContainText("Edit plan");
  await expect(page.locator("#bc-mode")).toContainText("EDIT PLAN");
  await expect(page.locator("#bc-pnl")).toContainText("Needs edit");
  await expect(page.locator("#bc-roi")).toContainText("No paper evidence");
  await expect(card).toContainText("Market data");
  await expect(card).toContainText("OK");
  await expect(card).toContainText("Entries needed");
  await expect(card).toContainText("Launchable setups");
  await expect(card).toContainText("Trades found");
  await expect(card).toContainText("Top blocker");
  await expect(card).toContainText("Next edit");
  await expect(card).not.toContainText("+0% ROI");
  await expect(page.locator("#bc-spark svg")).toHaveCount(0);
  await expect(page.locator("#g-live")).toBeHidden();
  await expect(page.locator("#g-try-auto")).toBeVisible();
  await expect(page.locator("#g-try-rsi")).toBeVisible();
  await page.click("#g-try-auto");
  await expect(page.locator("#g-strategy")).toHaveValue("auto");
});

test("trade tab shows the risk-budget form and pages navigate", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await expect(page.locator("#g-run")).toBeVisible();
  await expect(page.locator("#g-symbol")).toBeVisible();
  await expect(page.locator("#g-capital")).toBeVisible();
  await expect(page.locator("#g-profit")).toHaveCount(0);
  await expect(page.locator("#g-duration")).not.toContainText("∞ until stop");
  // The old order ticket is gone.
  await expect(page.locator("#side-long")).toHaveCount(0);

  await page.click('#nav button[data-view="history"]');
  await expect(page.locator("#view-history")).toBeVisible();
  await expect(page).toHaveURL(/\/history$/);

  await page.click('#nav button[data-view="settings"]');
  await expect(page.locator("#view-settings")).toBeVisible();
  await expect(page).toHaveURL(/\/settings$/);

  await page.click('#nav button[data-view="orders"]');
  await expect(page.locator("#view-orders")).toBeVisible();
});

test("malformed trade surfaces the parser's specific guidance", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  // Missing the "usdt" suffix on size — the parser explains exactly that.
  await page.fill("#cmd", "long BTC 3x entry 67500 sl 65000 tp 72000 size 100");
  await page.click("#cmd-run");
  await expect(page.locator("#cmd-out")).toContainText("usdt");
  await expect(page.locator("#cmd-out")).not.toContainText("Unknown command");
});

test("Ask ANNY types progressively and a new answer cancels the old one", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  const longAnswer = "FIRST ANSWER ".repeat(240);
  const secondAnswer = "SECOND ANSWER COMPLETE";
  await page.route("**/api/command", async (route) => {
    const body = JSON.parse(route.request().postData() || "{}");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ output: body.command === "second" ? secondAnswer : longAnswer }),
    });
  });

  await page.fill("#cmd", "first");
  await page.click("#cmd-run");
  await expect(page.locator("#cmd-out")).toBeVisible();
  await page.waitForTimeout(90);
  const partial = await page.locator("#cmd-out").innerText();
  expect(partial.length).toBeGreaterThan(0);
  expect(partial.length).toBeLessThan(longAnswer.length);

  await page.fill("#cmd", "second");
  await page.click("#cmd-run");
  await expect(page.locator("#cmd-out")).toHaveText(secondAnswer, { timeout: 4_000 });
});

test("flight recorder logs the paper run, labeled and hashed", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);
  await assessRiskPlan(page);

  await page.click('#nav button[data-view="history"]');
  await expect(page.locator("#view-history")).toBeVisible();
  // The run shows up in the Flight Recorder, tagged PAPER, with a Merkle root.
  await expect(page.locator("#rec-feed")).toContainText("BTCUSDT");
  await expect(page.locator("#rec-feed .flag.paper").first()).toBeVisible();
  await expect(page.locator("#rec-merkle")).toContainText("Merkle root");
  await expect(page.locator("#rec-stats")).toContainText("Paper runs");
});

test("community leaderboard aggregates the run; mission replay opens", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);
  await assessRiskPlan(page);

  // Community tab aggregates the paper run by strategy + coin.
  await page.click('#nav button[data-view="community"]');
  await expect(page.locator("#view-community")).toBeVisible();
  await expect(page.locator("#comm-strats")).toContainText("ema_cross");
  await expect(page.locator("#comm-coins")).toContainText("BTCUSDT");

  // Replay opens from a Mission in the Flight Recorder.
  await page.click('#nav button[data-view="history"]');
  await expect(page.locator("#view-history")).toBeVisible();
  await page.click("#rec-feed .mission.tap");
  await expect(page.locator("#replay")).toBeVisible();
  await expect(page.locator("#replay-steps")).toContainText("Verified hash");
});

test("goal run with AI toggle falls back gracefully (no key configured)", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await assessRiskPlan(page, { ai: true });

  // AI is not configured in the harness -> it must still produce a real run and
  // tell the user it used the rule-based strategy.
  await expect(page.locator("#g-card")).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("#g-msg")).toContainText("rule-based");
});

test("positive risk-budget run is launch ready with RR/edge transparency", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await assessRiskPlan(page);

  const card = page.locator("#g-card");
  await expect(card).toContainText("Positive result in this window");
  await expect(card).not.toContainText("Target reached");
  const stats = page.locator("#bc-stats");
  // Transparency cells: structural reward:risk and the per-trade paper average.
  await expect(stats).toContainText("RR");
  await expect(stats).toContainText("1 : 2");
  await expect(stats).toContainText("Paper avg / trade");
  await expect(stats).toContainText("USDT");
  await expect(stats).toContainText("Rule eligibility");
  await expect(stats).toContainText("Eligible");
  await expect(page.locator("#g-edit")).toHaveText("Update plan");
  await expect(page.locator("#g-live")).toHaveText("Next →");
});

test("editing the plan after assessment blocks Launch until re-assessed", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await assessRiskPlan(page);

  // Change the plan AFTER assessing, then try to Launch: the guard must block so
  // the paper evidence and the live mission can never drift apart.
  await page.fill("#g-capital", "200");
  await page.click("#g-live");
  await expect(page.locator("#g-msg")).toContainText("changed after the paper assessment");
});

test("testnet mission disarm clears running status and shows what happens next", async ({ page }) => {
  const token = await telegramLogin(page);
  await addTestnetKey(page, token);
  await openWithToken(page, token, "/orders");

  await assessRiskPlan(page);
  await expect(page.locator("#g-live")).toBeVisible();

  await page.click("#g-live");
  await expect(page.locator("#confirm-modal")).toBeVisible();
  await page.click("#confirm-modal-ok");

  await expect(page.locator("#g-confirm-out")).toContainText("within your risk budget", { timeout: 10_000 });
  await expect(page.locator("#g-campaign-status")).toContainText("Mission · BTCUSDT");
  await expect(page.locator("#g-campaign-status")).toContainText("waiting for a setup");
  await expect(page.locator("#g-campaign-actions")).toBeVisible();

  await page.click("#g-campaign-disarm");
  await expect(page.locator("#g-campaign-status")).toBeHidden();
  await expect(page.locator("#toast")).toContainText("Mission disarmed");
  await expect(page.locator("#positions")).toContainText("No open position", { timeout: 5_000 });
});

test("logout bounces back to the landing page and resets the URL", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page); // leave the address bar on a deep view (/orders)
  await expect(page).toHaveURL(/\/orders$/);

  await page.click("#logout");

  await expect(page.locator("#view-login")).toBeVisible();
  await expect(page.locator("#nav")).toBeHidden();
  await expect(page.locator("#logout")).toBeHidden();
  // The URL must return to the root, not stay on the deep view.
  await expect(page).toHaveURL(/\/$/);
});
