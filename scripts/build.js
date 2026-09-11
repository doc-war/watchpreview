const { execFileSync } = require("node:child_process");
const { mkdirSync } = require("node:fs");
const path = require("node:path");

const ROOT = path.resolve(__dirname, "..");
const OUT_DIR = path.join(ROOT, "dist");

const TARGETS = [
  { goos: "windows", goarch: "amd64", ext: ".exe" },
  { goos: "darwin", goarch: "amd64", ext: "" },
  { goos: "darwin", goarch: "arm64", ext: "" },
  { goos: "linux", goarch: "amd64", ext: "" },
  { goos: "linux", goarch: "arm64", ext: "" },
];

const HOST_GOOS = { win32: "windows", darwin: "darwin", linux: "linux" }[process.platform];
const HOST_GOARCH = { x64: "amd64", arm64: "arm64", ia32: "386" }[process.arch];

function goBuild(target) {
  const out = path.join(OUT_DIR, `watchpreview-${target.goos}-${target.goarch}${target.ext}`);
  console.log(`\n==> building ${path.relative(ROOT, out)}`);
  execFileSync("go", ["build", "-o", out, "./src"], {
    cwd: ROOT,
    env: { ...process.env, GOOS: target.goos, GOARCH: target.goarch },
    stdio: "inherit",
  });
  return out;
}

function runContractGuard(exe) {
  console.log(`\n==> contract guard test against ${path.relative(ROOT, exe)}`);
  execFileSync(
    "powershell",
    ["-ExecutionPolicy", "Bypass", "-File", path.join(ROOT, "test", "integration.ps1"), "-Exe", exe],
    { cwd: ROOT, stdio: "inherit" }
  );
}

mkdirSync(OUT_DIR, { recursive: true });

let hostArtifact = null;
for (const target of TARGETS) {
  const out = goBuild(target);
  if (target.goos === HOST_GOOS && target.goarch === HOST_GOARCH) {
    hostArtifact = out;
  }
}

if (hostArtifact) {
  runContractGuard(hostArtifact);
} else {
  console.log("\n(skipped contract test: host platform not in the build matrix)");
}

console.log("\nAll cross-platform builds done.");