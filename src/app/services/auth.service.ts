import { Injectable, signal } from '@angular/core';
import { Router } from '@angular/router';

export interface User {
  name: string;
  email: string;
}

@Injectable({
  providedIn: 'root'
})
export class AuthService {
  private readonly storageKey = 'angular_auth_user';

  readonly user = signal<User | null>(this.getStoredUser());

  constructor(private router: Router) {}

  login(email: string, password: string): boolean {
    // Demo authentication.
    // Replace this with your real API call.
    if (email === 'demo@example.com' && password === 'password') {
      const user: User = {
        name: 'Demo User',
        email
      };

      localStorage.setItem(this.storageKey, JSON.stringify(user));
      this.user.set(user);

      return true;
    }

    return false;
  }

  register(name: string, email: string, password: string): boolean {
    // Demo registration.
    const user: User = {
      name,
      email
    };

    localStorage.setItem(this.storageKey, JSON.stringify(user));
    this.user.set(user);

    return true;
  }

  logout(): void {
    localStorage.removeItem(this.storageKey);
    this.user.set(null);
    this.router.navigate(['/login']);
  }

  isAuthenticated(): boolean {
    return this.user() !== null;
  }

  private getStoredUser(): User | null {
    const stored = localStorage.getItem(this.storageKey);

    if (!stored) {
      return null;
    }

    try {
      return JSON.parse(stored);
    } catch {
      localStorage.removeItem(this.storageKey);
      return null;
    }
  }
}