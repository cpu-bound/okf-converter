# ---------- Build stage ----------
FROM node:22-alpine AS build

WORKDIR /app

# Install dependencies first for better Docker caching
COPY package*.json ./

RUN npm ci

# Copy application source
COPY . .

# Build Angular for production
RUN npm run build


# ---------- Runtime stage ----------
FROM nginx:1.29-alpine

# Remove default nginx content
RUN rm -rf /usr/share/nginx/html/*

# Angular's modern application builder outputs to dist/<project>/browser
COPY --from=build /app/dist/angular-auth/browser /usr/share/nginx/html

# SPA routing configuration
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]