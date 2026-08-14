import {
  Injectable,
  inject,
  signal
} from '@angular/core';

import {
  HttpClient
} from '@angular/common/http';

import {
  Router
} from '@angular/router';

import {
  Observable,
  catchError,
  map,
  of,
  tap
} from 'rxjs';

export interface User {
  id: string;
  name: string;
  email: string;
}

interface AuthResponse {
  user: User;
}

@Injectable({
  providedIn: 'root'
})
export class AuthService {
  private http = inject(HttpClient);
  private router = inject(Router);

  readonly user = signal<User | null>(null);

  loadCurrentUser(): Observable<User | null> {
    return this.http
      .get<AuthResponse>('/api/auth/me')
      .pipe(
        map(response => response.user),
        tap(user => this.user.set(user)),
        catchError(() => {
          this.user.set(null);
          return of(null);
        })
      );
  }

  login(
    email: string,
    password: string
  ): Observable<User> {
    return this.http
      .post<AuthResponse>('/api/auth/login', {
        email,
        password
      })
      .pipe(
        map(response => response.user),
        tap(user => this.user.set(user))
      );
  }

  register(
    name: string,
    email: string,
    password: string
  ): Observable<User> {
    return this.http
      .post<AuthResponse>('/api/auth/register', {
        name,
        email,
        password
      })
      .pipe(
        map(response => response.user),
        tap(user => this.user.set(user))
      );
  }

  logout(): void {
    this.http
      .post('/api/auth/logout', {})
      .subscribe({
        next: () => this.finishLogout(),
        error: () => this.finishLogout()
      });
  }

  private finishLogout(): void {
    this.user.set(null);
    this.router.navigate(['/login']);
  }
}
