import {
  bootstrapApplication
} from '@angular/platform-browser';

import {
  provideZonelessChangeDetection
} from '@angular/core';

import {
  provideRouter
} from '@angular/router';

import {
  provideHttpClient,
  withInterceptors
} from '@angular/common/http';

import {
  AppComponent
} from './app/app.component';

import {
  routes
} from './app/app.routes';

import {
  credentialsInterceptor
} from './app/interceptors/credentials.interceptor';

bootstrapApplication(
  AppComponent,
  {
    providers: [
      // This app ships without zone.js, so change detection must be told
      // explicitly to run zoneless - otherwise Angular has no consistent
      // scheduler and only re-renders on a handful of incidental triggers
      // (template event bindings), leaving state updated from async work
      // (HTTP responses handled in .subscribe()) invisible until some
      // unrelated event happens to trigger the next check.
      provideZonelessChangeDetection(),
      provideRouter(routes),
      provideHttpClient(withInterceptors([credentialsInterceptor]))
    ]
  }
).catch(console.error);