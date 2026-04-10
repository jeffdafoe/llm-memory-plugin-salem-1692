const { chromium } = require('playwright');
const path = require('path');
const fs = require('fs');
const https = require('https');
const http = require('http');

const ASSETS_DIR = path.join(__dirname, 'assets', 'tilesets');

// Simple URL fetch to file (follows redirects)
function fetchToFile(url, destPath) {
    return new Promise((resolve, reject) => {
        const proto = url.startsWith('https') ? https : http;
        proto.get(url, (res) => {
            if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
                return fetchToFile(res.headers.location, destPath).then(resolve).catch(reject);
            }
            const file = fs.createWriteStream(destPath);
            res.pipe(file);
            file.on('finish', () => { file.close(); resolve(); });
        }).on('error', reject);
    });
}

async function downloadOpenGameArt() {
    const url = 'https://opengameart.org/sites/default/files/tilesets_edit_5.zip';
    const dest = path.join(ASSETS_DIR, 'rgs-cc0', 'tileset.zip');
    console.log('Fetching:', url);
    await fetchToFile(url, dest);
    const stat = fs.statSync(dest);
    console.log('Downloaded OpenGameArt tileset:', stat.size, 'bytes');
}

async function downloadItchPack(browser, url, dir, name) {
    const page = await browser.newPage();
    await page.goto(url);

    // Click "Download Now"
    await page.click('a[href*="/purchase"]');
    await page.waitForTimeout(2000);

    // Look for "No thanks, just take me to the downloads"
    const noThanks = await page.$('a.direct_download_btn');
    if (noThanks) {
        console.log('Found direct download link');

        // Set up download listener before clicking
        const downloadPromise = page.waitForEvent('download', { timeout: 30000 });
        await noThanks.click();
        await page.waitForTimeout(3000);

        // Check if we got redirected to a download page with upload links
        const uploads = await page.$$('a.upload_name');
        if (uploads.length > 0) {
            console.log('On downloads page, found', uploads.length, 'files');
            const dlPromise = page.waitForEvent('download', { timeout: 30000 });
            await uploads[0].click();
            const download = await dlPromise;
            await download.saveAs(path.join(ASSETS_DIR, dir, name + '.zip'));
            console.log('Downloaded', name);
        } else {
            // Maybe the noThanks link triggered a direct download
            try {
                const download = await downloadPromise;
                await download.saveAs(path.join(ASSETS_DIR, dir, name + '.zip'));
                console.log('Downloaded', name, '(direct)');
            } catch (e) {
                console.log('No download triggered for', name);
                // Debug: show page content
                const pageUrl = page.url();
                console.log('Current URL:', pageUrl);
                const links = await page.$$eval('a', els => els.map(e => ({
                    href: e.href, text: e.textContent.trim().slice(0, 60), cls: e.className
                })).filter(l => l.cls.includes('download') || l.cls.includes('upload') || l.text.includes('ownload')));
                console.log('Download-related links:', JSON.stringify(links, null, 2));
            }
        }
    } else {
        console.log('No direct download link found for', name);
        const pageUrl = page.url();
        console.log('Current URL:', pageUrl);
    }
    await page.close();
}

(async () => {
    const browser = await chromium.launch({ headless: true });

    try {
        console.log('--- OpenGameArt 16x16 RPG Tileset ---');
        await downloadOpenGameArt();

        console.log('\n--- Seliel Mana Seed RPG Starter Pack ---');
        await downloadItchPack(browser, 'https://seliel-the-shaper.itch.io/rpg-starter-pack', 'seliel-village', 'mana-seed-starter');

        console.log('\n--- Mystic Woods ---');
        await downloadItchPack(browser, 'https://game-endeavor.itch.io/mystic-woods', 'mystic-woods', 'mystic-woods');
    } catch (err) {
        console.error('Error:', err.message);
    }

    await browser.close();
    console.log('\nDone!');
})();
