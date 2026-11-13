# Who can reach /health. It reports scanner lag, address count
# and the webhook queue; this endpoint has no auth.
#
# Restrict to monitoring:
#   allow 203.0.113.10;
#   deny  all;
allow all;
