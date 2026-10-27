#!/usr/bin/env node
"use strict";

const fs = require("fs");
const GuacamoleLite = require("guacamole-lite");

const keyFile = process.env.GUAC_CRYPT_FILE || "/tmp/guac-crypt.key";
const port = parseInt(process.env.GUAC_LISTEN_PORT || "8081", 10);
const guacdHost = process.env.GUACD_HOST || "127.0.0.1";
const guacdPort = parseInt(process.env.GUACD_PORT || "4822", 10);

function loadKey() {
  const fromEnv = (process.env.GUAC_CRYPT_KEY || "").trim();
  if (Buffer.byteLength(fromEnv) === 32) {
    return fromEnv;
  }
  try {
    const fromFile = fs.readFileSync(keyFile, "utf8").trim();
    if (Buffer.byteLength(fromFile) === 32) {
      return fromFile;
    }
  } catch (err) {
    if (err.code !== "ENOENT") {
      console.error(`guacamole key read failed: ${err.message}`);
    }
  }
  return "";
}

function start(key) {
  new GuacamoleLite(
    { host: "0.0.0.0", port },
    { host: guacdHost, port: guacdPort },
    {
      crypt: { cypher: "AES-256-CBC", key },
      log: { level: "NORMAL" },
      // Client-side idle limit. guacamole-lite also hardcodes 10s of silence
      // from guacd in GuacdClient.js; the image build raises that to match.
      maxInactivityTime: 900000,
    }
  );
  console.log(`guacamole-lite listening on ${port}, guacd ${guacdHost}:${guacdPort}`);
}

const ready = loadKey();
if (ready) {
  start(ready);
} else {
  console.log(`waiting for guacamole key at ${keyFile}`);
  const timer = setInterval(() => {
    const key = loadKey();
    if (!key) {
      return;
    }
    clearInterval(timer);
    start(key);
  }, 200);
}
