# Common proxying parameters for the Go scanner.
# proxy_pass is set in each location separately, as nginx requires.

proxy_set_header Host              $host;
# $remote_addr here is either the address the connection actually came from,
# or the client IP substituted by the realip module for trusted proxies
# (see /etc/nginx/geo/trusted-proxies.inc).
proxy_set_header X-Real-IP         $remote_addr;
# The header is ALWAYS overwritten entirely: the client value never reaches
# the application, and a spoofed IP cannot get into the logs.
proxy_set_header X-Forwarded-For   $remote_addr;
# Externally the service runs over https: TLS is terminated by the front proxy, and the
# connection reaches us already decrypted, on port 80. There is no other path to nginx
# except through the proxy, so the flag is set unconditionally.
proxy_set_header X-Forwarded-Proto https;
proxy_set_header Connection        "";

proxy_connect_timeout 5s;
proxy_send_timeout    60s;
# Historical sync (?sync=true) and transaction lists call
# external APIs; waiting minutes is normal here.
proxy_read_timeout    300s;

proxy_redirect off;
