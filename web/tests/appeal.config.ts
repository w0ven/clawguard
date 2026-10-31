import { defineConfig, devices } from '@playwright/test';
import path from 'node:path';
export default defineConfig({
 testDir:'./e2e', testMatch:['appeal-mini.spec.ts','appeal-admin.spec.ts'], fullyParallel:false,
 use:{baseURL:'http://127.0.0.1:3189', trace:'retain-on-failure', launchOptions:{executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || undefined}},
 webServer:{cwd:path.resolve(__dirname, '..'),command:'node node_modules/next/dist/bin/next dev --hostname 127.0.0.1 --port 3189',url:'http://127.0.0.1:3189',timeout:120000,reuseExistingServer:false},
 projects:[{name:'desktop',use:{...devices['Desktop Chrome']}},{name:'mobile',use:{...devices['Pixel 7']}}],
});
