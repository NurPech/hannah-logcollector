#!/usr/bin/env node

const { execSync } = require('child_process');
const fs = require('fs');
const https = require('https');
const path = require('path');
const readline = require('readline');

const CONFIG = {
    changelogFile: 'CHANGELOG.md',
    wipMarker: '## **WORK IN PROGRESS**',
    allowedBranches: ['main', 'master'],
    pollIntervalMs: 10_000,
};

const args = process.argv.slice(2);
const type = args.find(arg => ['major', 'minor', 'patch'].includes(arg.toLowerCase()));
const isDryRun = args.includes('--dry-run');
const isAutoConfirm = args.includes('--yes');

function run(command) {
    try {
        return execSync(command, { encoding: 'utf8', stdio: ['pipe', 'pipe', 'ignore'] }).trim();
    } catch {
        return '';
    }
}

function git(command) {
    execSync(command, { stdio: 'inherit' });
}

function exit(msg) {
    console.error(`\x1b[31m[Error] ${msg}\x1b[0m`);
    process.exit(1);
}

async function confirmStep(message) {
    if (isAutoConfirm) return true;
    return new Promise((resolve) => {
        const options = ['Yes', 'No'];
        let selectedIndex = 0;

        const render = () => {
            readline.cursorTo(process.stdout, 0);
            process.stdout.write(`${message} `);
            options.forEach((opt, i) => {
                const text = i === selectedIndex ? `\x1b[42m\x1b[30m ${opt} \x1b[0m` : `  ${opt}  `;
                process.stdout.write(text + ' ');
            });
        };

        process.stdin.setRawMode(true);
        process.stdin.resume();
        readline.emitKeypressEvents(process.stdin);
        render();

        const onKey = (str, key) => {
            if (key.name === 'left' || key.name === 'right') {
                selectedIndex = selectedIndex === 0 ? 1 : 0;
                render();
            } else if (key.name === 'return' || key.name === 'space') {
                process.stdin.setRawMode(false);
                process.stdin.pause();
                process.stdin.removeListener('keypress', onKey);
                console.log('\n');
                resolve(selectedIndex === 0);
            } else if (key.ctrl && key.name === 'c') {
                process.exit();
            }
        };
        process.stdin.on('keypress', onKey);
    });
}

// ---------------------------------------------------------------------------
// .env
// ---------------------------------------------------------------------------

function loadEnv() {
    const envPath = path.resolve(process.cwd(), '.env');
    if (!fs.existsSync(envPath)) return;
    for (const line of fs.readFileSync(envPath, 'utf8').split('\n')) {
        const match = line.match(/^([A-Z_][A-Z0-9_]*)=(.*)$/);
        if (match && !process.env[match[1]]) {
            process.env[match[1]] = match[2].trim().replace(/^["']|["']$/g, '');
        }
    }
}

// ---------------------------------------------------------------------------
// GitLab API
// ---------------------------------------------------------------------------

function deriveGitLabInfo() {
    const remote = run('git remote get-url origin');
    const match = remote.match(/https?:\/\/([^/]+)\/(.+?)(?:\.git)?$/);
    if (!match) exit('Remote-URL konnte nicht geparst werden.');
    return {
        baseUrl: `https://${match[1]}`,
        projectId: encodeURIComponent(match[2]),
    };
}

function apiRequest(baseUrl, token, method, apiPath, body = null) {
    return new Promise((resolve, reject) => {
        const url = new URL(`${baseUrl}/api/v4${apiPath}`);
        const bodyStr = body ? JSON.stringify(body) : null;
        const req = https.request({
            hostname: url.hostname,
            path: url.pathname + url.search,
            method,
            headers: {
                'PRIVATE-TOKEN': token,
                'Content-Type': 'application/json',
                ...(bodyStr ? { 'Content-Length': Buffer.byteLength(bodyStr) } : {}),
            },
        }, (res) => {
            let data = '';
            res.on('data', chunk => data += chunk);
            res.on('end', () => {
                try { resolve({ status: res.statusCode, body: JSON.parse(data) }); }
                catch { resolve({ status: res.statusCode, body: data }); }
            });
        });
        req.on('error', reject);
        if (bodyStr) req.write(bodyStr);
        req.end();
    });
}

async function createMergeRequest(baseUrl, token, projectId, sourceBranch, title) {
    const res = await apiRequest(baseUrl, token, 'POST', `/projects/${projectId}/merge_requests`, {
        source_branch: sourceBranch,
        target_branch: 'master',
        title,
        squash: true,
        remove_source_branch: true,
    });
    if (res.status !== 201) exit(`MR-Erstellung fehlgeschlagen (${res.status}): ${JSON.stringify(res.body)}`);
    return res.body;
}

async function waitForPipeline(baseUrl, token, projectId, mrIid) {
    process.stdout.write('Warte auf Pipeline-Start');
    for (let i = 0; i < 30; i++) {
        await new Promise(r => setTimeout(r, CONFIG.pollIntervalMs));
        const res = await apiRequest(baseUrl, token, 'GET',
            `/projects/${projectId}/merge_requests/${mrIid}`,
        );
        if (res.body.head_pipeline) { console.log(' \x1b[32m✔\x1b[0m'); return; }
        process.stdout.write('.');
    }
    console.log('');
    exit('Pipeline wurde nicht innerhalb von 5 Minuten gestartet.');
}

async function setAutoMerge(baseUrl, token, projectId, mrIid) {
    const res = await apiRequest(baseUrl, token, 'PUT',
        `/projects/${projectId}/merge_requests/${mrIid}/merge`,
        { merge_when_pipeline_succeeds: true },
    );
    if (res.status !== 200 && res.status !== 201) {
        exit(`Auto-Merge fehlgeschlagen (${res.status}): ${JSON.stringify(res.body)}`);
    }
}

async function pollUntilMerged(baseUrl, token, projectId, mrIid) {
    process.stdout.write('Warte auf Merge');
    while (true) {
        await new Promise(r => setTimeout(r, CONFIG.pollIntervalMs));
        const res = await apiRequest(baseUrl, token, 'GET',
            `/projects/${projectId}/merge_requests/${mrIid}`,
        );
        const state = res.body.state;
        if (state === 'merged') { console.log(' \x1b[32m✔\x1b[0m'); return; }
        if (state === 'closed') { console.log(''); exit('MR wurde geschlossen ohne Merge.'); }
        process.stdout.write('.');
    }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function start() {
    loadEnv();

    if (!type) exit('Gebrauch: node release.js <major|minor|patch> [--dry-run]');

    const currentBranch = run('git rev-parse --abbrev-ref HEAD');
    if (!CONFIG.allowedBranches.includes(currentBranch)) {
        exit(`RELEASING VERBOTEN: Du bist auf '${currentBranch}'. Releases sind nur erlaubt auf: ${CONFIG.allowedBranches.join(' oder ')}.`);
    }

    const { baseUrl, projectId } = deriveGitLabInfo();
    const token = process.env.GITLAB_TOKEN;
    if (!isDryRun && !token) exit('GITLAB_TOKEN nicht gesetzt (in .env oder Umgebungsvariable).');

    // CHANGELOG lesen
    const changelogPath = path.resolve(process.cwd(), CONFIG.changelogFile);
    if (!fs.existsSync(changelogPath)) exit(`${CONFIG.changelogFile} existiert nicht.`);
    const originalContent = fs.readFileSync(changelogPath, 'utf8');
    const wipRegex = /^## \*\*WORK IN PROGRESS\*\*/m;
    if (!wipRegex.test(originalContent)) exit(`WIP-Marker '${CONFIG.wipMarker}' nicht gefunden.`);

    // Version bestimmen
    const latestTag = run('git describe --tags --abbrev=0') || 'v0.0.0';
    const versionMatch = latestTag.match(/v?(\d+)\.(\d+)\.(\d+)/);
    if (!versionMatch) exit(`Ungültiges Tag-Format: ${latestTag}`);
    let [, major, minor, patch] = versionMatch.map(Number);
    if (type === 'major') { major++; minor = 0; patch = 0; }
    else if (type === 'minor') { minor++; patch = 0; }
    else { patch++; }
    const newVersion = `${major}.${minor}.${patch}`;
    const newTag = `v${newVersion}`;
    const releaseBranch = `release/${newTag}`;

    console.log(`\x1b[33mPlanung: ${latestTag} -> ${newTag}${isDryRun ? ' (DRY-RUN)' : ''}\x1b[0m`);

    // CHANGELOG aktualisieren
    const wipMatch = originalContent.match(wipRegex);
    const wipIndex = wipMatch.index;
    let nextVersionIndex = originalContent.slice(wipIndex + CONFIG.wipMarker.length).search(/\n## \d/);
    nextVersionIndex = nextVersionIndex === -1
        ? originalContent.length
        : nextVersionIndex + wipIndex + CONFIG.wipMarker.length;

    const wipEntries = originalContent.substring(wipIndex + CONFIG.wipMarker.length, nextVersionIndex).trim();
    const replacement = `${CONFIG.wipMarker}\n\n## ${newVersion}\n${wipEntries ? wipEntries + '\n' : ''}`;
    const newContent = originalContent.replace(
        originalContent.substring(wipIndex, nextVersionIndex),
        replacement,
    );

    if (isDryRun) {
        console.log(`\x1b[34m--- VORSCHAU ${CONFIG.changelogFile} ---\x1b[0m`);
        console.log(replacement);
        console.log(`\x1b[34m--- DRY-RUN: kein Commit, kein Push, kein MR ---\x1b[0m`);
        return;
    }

    fs.writeFileSync(changelogPath, newContent);
    console.log(`\x1b[32m✔ ${CONFIG.changelogFile} aktualisiert.\x1b[0m`);

    const ok = await confirmStep('Bitte Änderungen prüfen:');
    if (!ok) {
        fs.writeFileSync(changelogPath, originalContent);
        console.log('Abgebrochen. Changelog wiederhergestellt.');
        process.exit(0);
    }

    // Release-Branch, Commit, Push
    try {
        git(`git checkout -b ${releaseBranch}`);
        git(`git add ${CONFIG.changelogFile}`);
        git(`git commit -m "chore: bump to version ${newVersion}"`);
        git(`git push -u origin ${releaseBranch}`);
    } catch {
        console.error('\x1b[33mFehler beim Branch-Push — Rollback...\x1b[0m');
        run('git reset --hard HEAD~1');
        run(`git checkout ${currentBranch}`);
        run(`git branch -D ${releaseBranch}`);
        fs.writeFileSync(changelogPath, originalContent);
        exit('Rollback abgeschlossen. Lokaler Zustand wiederhergestellt.');
    }

    git(`git checkout ${currentBranch}`);

    // MR erstellen + Auto-Merge
    console.log('Erstelle Merge Request...');
    const mr = await createMergeRequest(baseUrl, token, projectId, releaseBranch,
        `chore: bump to version ${newVersion}`);
    console.log(`\x1b[32m✔ MR erstellt: ${mr.web_url}\x1b[0m`);
    await waitForPipeline(baseUrl, token, projectId, mr.iid);
    await setAutoMerge(baseUrl, token, projectId, mr.iid);
    console.log(`\x1b[32m✔ Auto-Merge aktiviert.\x1b[0m`);

    // Warten bis gemergt
    await pollUntilMerged(baseUrl, token, projectId, mr.iid);

    // Pull, Tag, Push
    git('git pull');
    git(`git tag -a ${newTag} -m "Release ${newVersion}"`);
    git(`git push origin ${newTag}`);
    run(`git branch -d ${releaseBranch}`);

    console.log(`\x1b[32m🚀 Release ${newTag} erfolgreich veröffentlicht!\x1b[0m`);
}

start();
