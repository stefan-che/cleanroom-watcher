module.exports = {
  apps: [
    {
      name: 'cleanroom-watcher',
      script: './scanner',
      cwd: '/opt/cleanroom-watcher',

      // A Go binary does not need cluster mode
      instances: 1,
      exec_mode: 'fork',

      // Auto-restart on crash
      autorestart: true,
      restart_delay: 5000,   // 5 s before restarting
      max_restarts: 10,      // stop after 10 consecutive crashes
      min_uptime: '10s',     // a start counts as successful after 10 s of uptime

      // Logs
      out_file: '/var/log/cleanroom-watcher/out.log',
      error_file: '/var/log/cleanroom-watcher/error.log',
      merge_logs: true,
      log_date_format: 'YYYY-MM-DD HH:mm:ss Z',

      // PM2 caches env on first start and does not re-read ecosystem.config.js
      // on pm2 restart. To pick up env changes, use:
      //   pm2 startOrReload ecosystem.config.js
      //
      // CONFIG_FILE is the path to config.json, read by the Go app on every start.
      // To update the node config, edit the file and restart the process.
      env: {
        CONFIG_FILE: '/opt/cleanroom-watcher/config.json'
      }
    }
  ]
}