# Build the standalone CRM browser application with its configuration baked
# into the static bundle. These are public browser values, never secrets.
FROM node:22.23.2-alpine3.24@sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32 AS build
WORKDIR /workspace

COPY package.json package-lock.json ./
COPY packages/contracts/package.json packages/contracts/package.json
COPY apps/web/package.json apps/web/package.json
RUN npm ci

COPY packages/contracts packages/contracts
COPY apps/web apps/web

ARG VITE_CRM_API_URL=http://localhost:19431
ARG VITE_CRM_AUTH_PORTAL_URL=http://localhost:4173/?auth=login#top
ENV VITE_CRM_API_URL=$VITE_CRM_API_URL
ENV VITE_CRM_AUTH_PORTAL_URL=$VITE_CRM_AUTH_PORTAL_URL
RUN npm run build --workspace=@crm/web

FROM nginx:1.31.5-alpine3.24@sha256:72ba65eb42c10344912a84ff42408db7d34f2feb642204570ab8fc5ffd29f1d3
RUN rm -f /etc/nginx/conf.d/default.conf && \
    chown -R 101:101 /var/cache/nginx /usr/share/nginx/html
COPY apps/web/nginx-main.conf /etc/nginx/nginx.conf
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build --chown=101:101 /workspace/apps/web/dist /usr/share/nginx/html
USER 101:101
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1
ENTRYPOINT ["nginx", "-g", "daemon off;"]
