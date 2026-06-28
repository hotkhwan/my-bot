import { test, expect, Page } from "@playwright/test";
import * as fs from "fs";

// Capture full-page screenshots of each view for visual review. Output to
// e2e/screens/*.png (gitignored).
const OUT = "screens";

async function login(page: Page) {
  await page.goto("/");
  const user = "shot_" + Date.now();
  await page.fill("#username", user);
  await page.fill("#password", "password123");
  await Promise.all([page.waitForResponse((r) => r.url().includes("/api/register")), page.click("#register")]);
  await Promise.all([page.waitForResponse((r) => r.url().includes("/api/login")), page.click("#login")]);
  await expect(page.locator("#view-home")).toBeVisible();
}

test("capture all screens", async ({ page }) => {
  fs.mkdirSync(OUT, { recursive: true });
  await page.setViewportSize({ width: 420, height: 900 });

  await page.goto("/");
  await page.screenshot({ path: `${OUT}/00-login.png`, fullPage: true });

  await login(page);
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${OUT}/01-home.png`, fullPage: true });

  await page.click('#nav button[data-view="orders"]');
  await expect(page.locator("#view-orders")).toBeVisible();
  // Run a goal so the Trade screenshot shows real stats.
  await page.fill("#g-profit", "5");
  await page.selectOption("#g-symbol", "BTC");
  await page.click("#g-run");
  await expect(page.locator("#g-card")).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(300);
  await page.screenshot({ path: `${OUT}/02-trade.png`, fullPage: true });

  await page.click('#nav button[data-view="history"]');
  await expect(page.locator("#view-history")).toBeVisible();
  await page.waitForTimeout(300);
  await page.screenshot({ path: `${OUT}/03-history.png`, fullPage: true });

  await page.click('#nav button[data-view="community"]');
  await expect(page.locator("#view-community")).toBeVisible();
  await page.waitForTimeout(300);
  await page.screenshot({ path: `${OUT}/04-community.png`, fullPage: true });

  await page.click('#nav button[data-view="settings"]');
  await expect(page.locator("#view-settings")).toBeVisible();
  await page.waitForTimeout(300);
  await page.screenshot({ path: `${OUT}/05-settings.png`, fullPage: true });

  // Mission Replay overlay.
  await page.click('#nav button[data-view="history"]');
  await expect(page.locator("#view-history")).toBeVisible();
  await page.click("#rec-feed .mission.tap");
  await expect(page.locator("#replay")).toBeVisible();
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${OUT}/06-replay.png`, fullPage: true });
});
