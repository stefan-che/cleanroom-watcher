module.exports = {
  apps: [
    {
      name: 'cleanroom-watcher',
      script: './scanner',
      cwd: '/var/www/listener',

      instances: 1,
      exec_mode: 'fork',

      autorestart: true,
      restart_delay: 5000,
      max_restarts: 10,
      min_uptime: '10s',

      out_file: '/var/log/cleanroom-watcher/out.log',
      error_file: '/var/log/cleanroom-watcher/error.log',
      merge_logs: true,
      log_date_format: 'YYYY-MM-DD HH:mm:ss Z',

      env: {
        CONFIG_FILE: '/var/www/listener/config.json'
      }
    }
  ]
}
