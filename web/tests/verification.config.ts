import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './e2e', fullyParallel: false, workers: 1, retries: 0,
  timeout: 30000, expect: { timeout: 5000 },
  outputDir: '../../../artifacts/clawguard-ui-implementation/verification/test-results',
  reporter: [['list'], ['json', {outputFile: '../../../artifacts/clawguard-ui-implementation/verification/playwright.json'}]],
  use: {baseURL:'http://127.0.0.1:3217', trace:'retain-on-failure', video:'retain-on-failure', screenshot:'only-on-failure'},
  webServer: {command:'node node_modules/next/dist/bin/next start --hostname 127.0.0.1 --port 3217', cwd: process.cwd(), url:'http://127.0.0.1:3217', reuseExistingServer:false, timeout:60000, gracefulShutdown:{signal:'SIGTERM',timeout:5000}},
  projects: [{name:'desktop',use:{browserName:'chromium',viewport:{width:1440,height:1000}}},{name:'mobile',use:{browserName:'chromium',viewport:{width:390,height:844},isMobile:true,hasTouch:true}}]
});
