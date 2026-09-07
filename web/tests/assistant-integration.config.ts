import {defineConfig} from '@playwright/test';
import fs from 'node:fs';
const sessionFile=process.env.CG_BROWSER_IT_SESSION;
if(!sessionFile)throw new Error('Run tests/run-assistant-integration.py; a private local runtime session is required.');
const session=JSON.parse(fs.readFileSync(sessionFile,'utf8'));
const out=process.env.CG_BROWSER_IT_OUT!;
export default defineConfig({testDir:'./integration',fullyParallel:false,workers:1,retries:0,timeout:90000,expect:{timeout:6000},reporter:[['list'],['json',{outputFile:`${out}/playwright.json`}]],outputDir:`${out}/test-results`,use:{baseURL:session.baseURL,browserName:'chromium',viewport:{width:1440,height:1000},trace:'retain-on-failure',screenshot:'only-on-failure'}});
