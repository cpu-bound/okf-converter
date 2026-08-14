import { inject } from '@angular/core';

import {
  CanActivateFn,
  Router
} from '@angular/router';

import {
  AuthService
} from '../services/auth.service';

import {
  map
} from 'rxjs';

export const authGuard: CanActivateFn = () => {
  const auth = inject(AuthService);
  const router = inject(Router);

  if (auth.user()) {
    return true;
  }

  return auth
    .loadCurrentUser()
    .pipe(
      map(user => {
        if (user) {
          return true;
        }

        return router.createUrlTree([
          '/login'
        ]);
      })
    );
};