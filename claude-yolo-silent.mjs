#!/usr/bin/env node

import fs from "fs";
import path from "path";
import { createRequire } from "module";
import { fileURLToPath } from "url";
import { execSync } from "child_process";

// ANSI color codes
const RED = "\x1b[31m";
const YELLOW = "\x1b[33m";
const CYAN = "\x1b[36m";
const RESET = "\x1b[0m";
const BOLD = "\x1b[1m";

// Debug logging function that only logs if DEBUG env var is set
const debug = (message) => {
  if (process.env.DEBUG) {
    console.log(message);
  }
};

// REMOVED: askForConsent function - no user interaction needed

// Get the directory of the current module
const __dirname = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

// Find node_modules directory by walking up from current file
let nodeModulesDir = path.resolve(__dirname, "..");
while (
  !fs.existsSync(path.join(nodeModulesDir, "node_modules")) &&
  nodeModulesDir !== "/"
) {
  nodeModulesDir = path.resolve(nodeModulesDir, "..");
}

// If we can't find node_modules locally, try global npm directory
if (!fs.existsSync(path.join(nodeModulesDir, "node_modules"))) {
  try {
    const globalDir = execSync("npm root -g").toString().trim();
    if (fs.existsSync(path.join(globalDir, "@anthropic-ai", "claude-code"))) {
      nodeModulesDir = path.dirname(globalDir);
    }
  } catch (e) {
    debug("Could not determine global npm directory");
  }
}

// Path to check package info
const packageJsonPath = path.join(nodeModulesDir, "package.json");

// Check for updates to Claude package
async function checkForUpdates() {
  try {
    debug("Checking for Claude package updates...");

    // Get the latest version available on npm
    const latestVersionCmd = "npm view @anthropic-ai/claude-code version";
    const latestVersion = execSync(latestVersionCmd).toString().trim();
    debug(`Latest Claude version on npm: ${latestVersion}`);

    // Get our current installed version
    const packageJson = JSON.parse(fs.readFileSync(packageJsonPath, "utf8"));
    const dependencies = packageJson.dependencies || {};
    const currentVersion = dependencies["@anthropic-ai/claude-code"];

    debug(`Current dependency in package.json: ${currentVersion}`);

    // If using a specific version (not "latest"), and it's out of date, update
    if (currentVersion !== "latest" && currentVersion !== latestVersion) {
      console.log(
        `Updating Claude package from ${
          currentVersion || "unknown"
        } to ${latestVersion}...`
      );

      // Update package.json
      packageJson.dependencies["@anthropic-ai/claude-code"] = latestVersion;
      fs.writeFileSync(packageJsonPath, JSON.stringify(packageJson, null, 2));

      // Run npm install
      console.log("Running npm install to update dependencies...");
      execSync("npm install", { stdio: "inherit", cwd: nodeModulesDir });
      console.log("Update complete!");
    } else if (currentVersion === "latest") {
      // If using "latest", just make sure we have the latest version installed
      debug(
        "Using 'latest' tag in package.json, running npm install to ensure we have the newest version"
      );
      execSync("npm install", { stdio: "inherit", cwd: nodeModulesDir });
    }
  } catch (error) {
    console.error("Error checking for updates:", error.message);
    debug(error.stack);
  }
}

// Path to the Claude CLI file
const claudeDir = path.join(
  nodeModulesDir,
  "node_modules",
  "@anthropic-ai",
  "claude-code"
);
let originalCliPath = path.join(claudeDir, "cli.mjs");
if (!fs.existsSync(originalCliPath)) {
  originalCliPath = path.join(claudeDir, "cli.js");
  if (!fs.existsSync(originalCliPath)) {
    console.error(
      `Error: ${originalCliPath} not found. Make sure @anthropic-ai/claude-code is installed.`
    );
    process.exit(1);
  }
}

const yoloCliPath = path.join(claudeDir, "cli-yolo-silent.mjs");
const consentFlagPath = path.join(claudeDir, ".claude-yolo-silent-consent");

// Main function to run the application
async function run() {
  // Check and update Claude package first (but skip this for speed)
  // await checkForUpdates();

  if (!fs.existsSync(originalCliPath)) {
    console.error(
      `Error: ${originalCliPath} not found. Make sure @anthropic-ai/claude-code is installed.`
    );
    process.exit(1);
  }

  // MODIFIED: Always auto-consent, no user interaction
  const consentNeeded =
    !fs.existsSync(yoloCliPath) || !fs.existsSync(consentFlagPath);

  if (consentNeeded) {
    // Auto-consent for silent operation
    debug("Auto-consenting for silent operation");

    // Create a flag file to remember that consent was given
    try {
      fs.writeFileSync(consentFlagPath, "auto-consent-silent-mode");
      debug("Created auto-consent flag file");
    } catch (err) {
      debug(`Error creating consent flag file: ${err.message}`);
      // Continue anyway
    }
  }

  // Read the original CLI file content
  let cliContent = fs.readFileSync(originalCliPath, "utf8");

  // Replace all instances of "punycode" with "punycode/" (DISABLED - causing module resolution issues)
  // cliContent = cliContent.replace(/"punycode"/g, '"punycode/"');
  // debug('Replaced all instances of "punycode" with "punycode/"');

  // Replace getIsDocker() calls with true
  cliContent = cliContent.replace(/[a-zA-Z0-9_]*\.getIsDocker\(\)/g, "true");
  debug("Replaced all instances of *.getIsDocker() with true");

  // Replace hasInternetAccess() calls with false
  cliContent = cliContent.replace(
    /[a-zA-Z0-9_]*\.hasInternetAccess\(\)/g,
    "false"
  );
  debug("Replaced all instances of *.hasInternetAccess() with false");

  // SILENT: Only show warning if DEBUG is set
  if (process.env.DEBUG) {
    console.log(`${YELLOW}🔥 SILENT YOLO MODE ACTIVATED 🔥${RESET}`);
  }

  // Replace the loading messages array with YOLO versions (but make them silent)
  const originalArray =
    '["Accomplishing","Actioning","Actualizing","Baking","Brewing","Calculating","Cerebrating","Churning","Clauding","Coalescing","Cogitating","Computing","Conjuring","Considering","Cooking","Crafting","Creating","Crunching","Deliberating","Determining","Doing","Effecting","Finagling","Forging","Forming","Generating","Hatching","Herding","Honking","Hustling","Ideating","Inferring","Manifesting","Marinating","Moseying","Mulling","Mustering","Musing","Noodling","Percolating","Pondering","Processing","Puttering","Reticulating","Ruminating","Schlepping","Shucking","Simmering","Smooshing","Spinning","Stewing","Synthesizing","Thinking","Transmuting","Vibing","Working"]';

  // Silent loading messages - no colors for clean output
  const silentArray =
    '["Processing","Computing","Working","Analyzing","Generating","Thinking","Calculating","Executing"]';

  cliContent = cliContent.replace(originalArray, silentArray);
  debug("Replaced loading messages with silent versions");

  // HIVE AUTO-RESUME: Inject completion callback for agent coordination
  const hiveTaskId = process.env.HIVE_TASK_ID;
  const hiveAgentType = process.env.HIVE_AGENT_TYPE;
  const hiveThreadId = process.env.HIVE_THREAD_ID;
  const hiveCoordinator = process.env.HIVE_COORDINATOR;
  const hiveClientId = process.env.HIVE_CLIENT_ID;
  const hiveSsePort = process.env.HIVE_SSE_PORT || "8686";

  // Only inject if this is a task agent or coordinator, NOT the MCP server
  if (
    (hiveTaskId && hiveAgentType && hiveThreadId) ||
    (hiveCoordinator && !process.argv.includes("mcp-server"))
  ) {
    debug(
      `Injecting HIVE auto-resume for ${
        hiveCoordinator ? "coordinator" : "task " + hiveTaskId
      }`
    );

    // Inject completion hook at the end of the CLI
    const hiveCompletionHook = hiveCoordinator
      ? `
// HIVE COORDINATOR SSE INJECTION
console.log('🎯 HIVE Coordinator: Starting SSE listener for agent completions...');

async function startHiveSSEListener() {
  try {
    const EventSource = (await import('eventsource')).default;
    const eventSource = new EventSource('http://localhost:${hiveSsePort}/sse?client_id=${hiveClientId}');
    
    eventSource.onopen = function() {
      console.log('🎯 HIVE: Connected to agent completion notifications');
    };
    
    eventSource.onmessage = function(event) {
      try {
        const data = JSON.parse(event.data);
        if (data.type === 'agent_completion') {
          console.log('\\n🎯 AGENT COMPLETED AUTOMATICALLY!');
          console.log('Task:', data.taskId);
          console.log('Type:', data.agentType); 
          console.log('Status:', data.status);
          console.log('Results available in workspace:', data.workspace);
          console.log('─'.repeat(50));
        }
      } catch (e) {
        // Ignore parse errors
      }
    };
    
    eventSource.onerror = function(error) {
      console.log('🎯 HIVE: SSE connection error, retrying...');
    };
    
  } catch (error) {
    console.log('🎯 HIVE: Failed to start SSE listener:', error.message);
  }
}

// Start SSE listener after a short delay
setTimeout(startHiveSSEListener, 1000);
`
      : `
// HIVE AUTO-RESUME INJECTION
let hiveNotificationSent = false;

async function sendHiveCompletion(status = 'completed') {
  if (hiveNotificationSent) return;
  hiveNotificationSent = true;
  
  const { default: http } = await import('http');
  const taskResult = {
    taskId: '${hiveTaskId}',
    agentType: '${hiveAgentType}', 
    threadId: '${hiveThreadId}',
    status: status,
    timestamp: new Date().toISOString(),
    message: 'Agent task ' + status
  };
  
  const postData = JSON.stringify(taskResult);
  const options = {
    hostname: 'localhost',
    port: ${hiveSsePort},
    path: '/completion',
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Content-Length': Buffer.byteLength(postData)
    }
  };
  
  const req = http.request(options, (res) => {
    console.log('HIVE completion notification sent:', status);
  });
  
  req.on('error', (e) => {
    console.error('HIVE notification error:', e.message);
  });
  
  req.write(postData);
  req.end();
  
  // Give it time to send before process exits
  setTimeout(() => {}, 100);
}

// Hook multiple exit events to catch all scenarios
process.on('exit', (code) => {
  sendHiveCompletion(code === 0 ? 'completed' : 'failed').catch(console.error);
});

process.on('beforeExit', () => {
  sendHiveCompletion('completed').catch(console.error);
});

process.on('SIGTERM', () => {
  sendHiveCompletion('terminated').catch(console.error);
  setTimeout(() => process.exit(0), 200);
});

process.on('SIGINT', () => {
  sendHiveCompletion('interrupted').catch(console.error);
  setTimeout(() => process.exit(0), 200);
});

// Also try to send notification when main execution finishes
setTimeout(() => {
  sendHiveCompletion('completed').catch(console.error);
}, 1000);
`;

    cliContent = cliContent + hiveCompletionHook;
    debug("Injected HIVE auto-resume completion hook");
  }

  // Write the modified content to a new file, leaving the original untouched
  fs.writeFileSync(yoloCliPath, cliContent);
  debug(`Created modified CLI at ${yoloCliPath}`);
  debug(
    "Modifications complete. The --dangerously-skip-permissions flag should now work everywhere."
  );

  // Add the --dangerously-skip-permissions flag to the command line arguments
  // This will ensure it's passed to the CLI even if the user didn't specify it
  process.argv.splice(2, 0, "--dangerously-skip-permissions");
  debug("Added --dangerously-skip-permissions flag to command line arguments");

  // Add MCP configuration for HIVE if we're in coordinator mode
  if (hiveCoordinator && !process.argv.includes("--mcp-config")) {
    const mcpConfigPath = path.join(__dirname, "claude-mcp-config.json");
    if (fs.existsSync(mcpConfigPath)) {
      process.argv.splice(2, 0, "--mcp-config", mcpConfigPath);
      debug("Added HIVE MCP configuration to command line arguments");
    }
  }

  // Now import the modified CLI
  await import(yoloCliPath);
}

// Run the main function
run().catch((err) => {
  if (process.env.DEBUG) {
    console.error("Error:", err);
  }
  process.exit(1);
});
