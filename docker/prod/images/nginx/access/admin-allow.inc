# Who can reach the web admin panel. The file is mounted from the host
# (docker/prod/images/nginx/access): edit it without rebuilding the image,
# apply with: make nginx-reload
#
# Recommended: keep only administrator IPs (admin allowlist):
#   allow 203.0.113.10;
#   allow 198.51.100.0/24;
#   deny  all;
#
# Open to everyone by default: the admin path is secret, login requires a password,
# and the login form is limited to 10 attempts per minute.
allow all;
