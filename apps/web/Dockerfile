# Build the standalone CRM browser application with its configuration baked
# into the static bundle. These are public browser values, never secrets: this
# is an Authorization Code + PKCE SPA client.
FROM node:22-alpine AS build
WORKDIR /workspace

COPY package.json package-lock.json ./
COPY packages/auth-client/package.json packages/auth-client/package.json
COPY packages/contracts/package.json packages/contracts/package.json
COPY apps/web/package.json apps/web/package.json
RUN npm ci

COPY packages/auth-client packages/auth-client
COPY packages/contracts packages/contracts
COPY apps/web apps/web

ARG VITE_AUTH_ISSUER_URL=http://localhost:18080
ARG VITE_AUTH_ORG_ID
ARG VITE_AUTH_CLIENT_ID=crm-web
ARG VITE_CRM_API_URL=http://localhost:19431
ENV VITE_AUTH_ISSUER_URL=$VITE_AUTH_ISSUER_URL \
    VITE_AUTH_ORG_ID=$VITE_AUTH_ORG_ID \
    VITE_AUTH_CLIENT_ID=$VITE_AUTH_CLIENT_ID \
    VITE_CRM_API_URL=$VITE_CRM_API_URL
RUN npm run build --workspace=@crm/web

FROM nginx:1.27-alpine
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /workspace/apps/web/dist /usr/share/nginx/html
EXPOSE 80
