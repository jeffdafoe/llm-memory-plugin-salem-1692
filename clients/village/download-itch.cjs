const { chromium } = require('playwright');
const path = require('path');

const ASSETS_DIR = path.join(__dirname, 'assets', 'tilesets');

async function downloadItchPack(browser, url, dir, name) {
    const page = await browser.newPage();
    await page.goto(url);

    // Click "Download Now" to get to purchase page
    await page.click('a[href*="/purchase"]');
    await page.waitForTimeout(2000);

    // Click "No thanks, just take me to the downloads"
    await page.click('a.direct_download_btn');
    await page.waitForTimeout(3000);

    // Now on the downloads page — click the Download button(s)
    const downloadBtns = await page.$$('a.button.download_btn');
    console.log(name + ': found', downloadBtns.length, 'download button(s)');

    for (let i = 0; i < downloadBtns.length; i++) {
        const label = await downloadBtns[i].evaluate(el => {
            const row = el.closest('.upload');
            if (row) {
                const nameEl = row.querySelector('.upload_name .name');
                return nameEl ? nameEl.textContent.trim() : 'file' + i;
            }
            return 'file' + i;
        });
        console.log('  Downloading:', label);

        const downloadPromise = page.waitForEvent('download', { timeout: 60000 });
        await downloadBtns[i].click();
        const download = await downloadPromise;
        const suggestedName = download.suggestedFilename();
        await download.saveAs(path.join(ASSETS_DIR, dir, suggestedName));
        console.log('  Saved as:', suggestedName);
    }

    await page.close();
}

(async () => {
    const browser = await chromium.launch({ headless: true });

    try {
        console.log('--- Seliel Mana Seed RPG Starter Pack ---');
        await downloadItchPack(browser, 'https://seliel-the-shaper.itch.io/rpg-starter-pack', 'seliel-village', 'mana-seed-starter');

        console.log('\n--- Mystic Woods ---');
        await downloadItchPack(browser, 'https://game-endeavor.itch.io/mystic-woods', 'mystic-woods', 'mystic-woods');
    } catch (err) {
        console.error('Error:', err.message);
    }

    await browser.close();
    console.log('\nDone!');
})();
