const path = require('path');

module.exports = {
    testDir: __dirname,
    timeout: 45_000,
    retries: 0,
    workers: 1,
    use: {
        baseURL: 'http://127.0.0.1:18765',
        browserName: 'chromium',
        headless: true
    },
    webServer: {
        command: [
            'go run ./cmd/netspeedd',
            '--listen 127.0.0.1:18765',
            '--web-dir ./web',
            '--cors=false',
            '--max-bytes 16777216',
            '--max-transfers 64',
            '--max-client-transfers 16',
            '--client-quota-bytes 0'
        ].join(' '),
        cwd: path.resolve(__dirname, '..', '..'),
        url: 'http://127.0.0.1:18765/health',
        timeout: 120_000,
        reuseExistingServer: false,
        stdout: 'pipe',
        stderr: 'pipe'
    }
};
