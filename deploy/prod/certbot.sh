#!/bin/bash

certbot certonly --webroot -w /var/www/listener -d listener.example.com --agree-tos -n --email admin@example.com --force-renewal

cp /etc/letsencrypt/live/listener.example.com-0001/fullchain.pem /etc/nginx/ssl/listener.example.com.crt
cp /etc/letsencrypt/live/listener.example.com-0001/privkey.pem /etc/nginx/ssl/listener.example.com.key

/usr/sbin/nginx -s reload
