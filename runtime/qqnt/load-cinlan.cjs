'use strict';

const path = require('node:path');

const officialMain = path.join(
  __dirname,
  'application.asar',
  'app_launcher',
  'index.js',
);
const runtimePath = process.env.CINLAN_QQNT_RUNTIME_PATH;

if (!runtimePath) {
  throw new Error('CINLAN_QQNT_RUNTIME_PATH is required');
}

require(officialMain);

setImmediate(() => {
  try {
    const runtime = require(runtimePath);
    Promise.resolve(runtime.start())
      .catch((error) => {
        console.error('[Cinlan QQ Runtime] startup failed:', error);
      });
  } catch (error) {
    console.error('[Cinlan QQ Runtime] load failed:', error);
  }
});
