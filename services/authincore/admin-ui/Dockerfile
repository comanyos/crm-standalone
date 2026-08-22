# Stage 1: Build
FROM node:22-alpine AS builder
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# Stage 2: Serve with nginx
FROM nginx:1.29-alpine
RUN apk upgrade --no-cache && \
	rm -f /etc/nginx/conf.d/default.conf && \
	chown -R 101:101 /var/cache/nginx /usr/share/nginx/html
COPY nginx-main.conf /etc/nginx/nginx.conf
COPY --from=builder --chown=101:101 /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
USER 101:101
EXPOSE 3001
ENTRYPOINT ["nginx", "-g", "daemon off;"]
