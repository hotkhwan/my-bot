import { test, expect, Page } from "@playwright/test";

// Each test registers a fresh user (password flow — no Telegram needed) so runs
// are independent against the in-memory user store.
async function registerAndLogin(page: Page) {
  await page.goto("/");
  await page.fill("#username", "e2e_user");
  await page.fill("#password", "password123");
  // Register creates the account (no session token); Log in then issues the JWT.
  // Await each response so the login never races ahead of the register write.
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/login")),
    page.click("#login"),
  ]);
  await expect(page.locator("#nav")).toBeVisible();
  // After login the default tab is Home (NOVA companion).
  await expect(page.locator("#view-home")).toBeVisible();
}

// Open the Trade tab, where the order ticket, goal form, and command console live.
async function gotoTrade(page: Page) {
  await page.click('#nav button[data-view="orders"]');
  await expect(page.locator("#view-orders")).toBeVisible();
}

test("login → goal paper run shows real stats", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await page.fill("#g-profit", "5");
  await page.fill("#g-capital", "100");
  await page.fill("#g-risk", "30");
  await page.selectOption("#g-symbol", "BTC");
  await page.selectOption("#g-strategy", "ema");
  await page.selectOption("#g-duration", "1h");
  await page.click("#g-run");

  const card = page.locator("#g-card");
  await expect(card).toBeVisible({ timeout: 20_000 });
  // Real, deterministic outcome on the stub uptrend: target reached at 100% WR.
  await expect(card).toContainText("Target reached");
  await expect(card).toContainText("100%");
  // Equity curve drawn, trades listed, and the run accumulates into history.
  await expect(page.locator("#bc-spark svg")).toBeVisible();
  await expect(page.locator("#g-history")).toContainText("BTCUSDT");
});

test("ANNY Basic no-setup asks for plan edit, not a zero paper result", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await page.fill("#g-profit", "10");
  await page.fill("#g-capital", "100");
  await page.fill("#g-risk", "60");
  await page.selectOption("#g-symbol", "BTC");
  await page.selectOption("#g-strategy", "anny_basic");
  await page.selectOption("#g-duration", "15m");
  await page.click("#g-run");

  const card = page.locator("#g-card");
  await expect(card).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("#g-card-title")).toContainText("Edit plan");
  await expect(page.locator("#bc-mode")).toContainText("EDIT PLAN");
  await expect(page.locator("#bc-pnl")).toContainText("Needs edit");
  await expect(page.locator("#bc-roi")).toContainText("No paper result");
  await expect(card).toContainText("Market data");
  await expect(card).toContainText("OK");
  await expect(card).toContainText("Est. entries");
  await expect(card).toContainText("Trades found");
  await expect(card).toContainText("No CDC/QQE setup");
  await expect(card).not.toContainText("+0% ROI");
  await expect(page.locator("#bc-spark svg")).toHaveCount(0);
  await expect(page.locator("#g-live")).toBeHidden();
});

test("trade tab shows the goal form and pages navigate", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);

  await expect(page.locator("#g-run")).toBeVisible();
  await expect(page.locator("#g-symbol")).toBeVisible();
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

test("flight recorder logs the paper run, labeled and hashed", async ({ page }) => {
  await registerAndLogin(page);
  await gotoTrade(page);
  await page.fill("#g-profit", "5");
  await page.selectOption("#g-symbol", "BTC");
  await page.selectOption("#g-strategy", "ema");
  await page.click("#g-run");
  await expect(page.locator("#g-card")).toBeVisible({ timeout: 20_000 });

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
  await page.fill("#g-profit", "5");
  await page.selectOption("#g-symbol", "BTC");
  await page.selectOption("#g-strategy", "ema");
  await page.click("#g-run");
  await expect(page.locator("#g-card")).toBeVisible({ timeout: 20_000 });

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

  await page.check("#g-ai");
  await page.fill("#g-profit", "5");
  await page.selectOption("#g-symbol", "BTC");
  await page.click("#g-run");

  // AI is not configured in the harness → it must still produce a real run and
  // tell the user it used the rule-based strategy.
  await expect(page.locator("#g-card")).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("#g-msg")).toContainText("rule-based");
});
