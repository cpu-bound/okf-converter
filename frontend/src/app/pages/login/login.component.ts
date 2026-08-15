import { Component, inject, signal } from '@angular/core';
import {
  FormBuilder,
  ReactiveFormsModule,
  Validators
} from '@angular/forms';
import { Router, RouterLink } from '@angular/router';
import { AuthService } from '../../services/auth.service';

@Component({
  selector: 'app-login',
  standalone: true,
  imports: [ReactiveFormsModule, RouterLink],
  templateUrl: './login.component.html'
})
export class LoginComponent {
  private fb = inject(FormBuilder);
  private auth = inject(AuthService);
  private router = inject(Router);

  // Signals, not plain fields: error/submitting are set from inside the
  // login() .subscribe() callback, which runs outside any template-bound
  // event - under zoneless change detection a plain field write there
  // would sit unrendered until some unrelated click triggered a check.
  showPassword = signal(false);
  submitting = signal(false);
  error = signal('');

  loginForm = this.fb.nonNullable.group({
    email: ['', [Validators.required, Validators.email]],
    password: ['', [Validators.required, Validators.minLength(8)]]
  });

  togglePasswordVisibility(): void {
    this.showPassword.update(shown => !shown);
  }

  submit(): void {
    this.error.set('');

    if (this.loginForm.invalid) {
      this.loginForm.markAllAsTouched();
      return;
    }

    const {
      email,
      password
    } = this.loginForm.getRawValue();

    this.submitting.set(true);

    this.auth
      .login(email, password)
      .subscribe({
        next: () => {
          this.router.navigate(['/dashboard']);
        },

        error: error => {
          this.submitting.set(false);
          this.error.set(
            error?.error?.message ||
            'No se pudo iniciar sesión.'
          );
        }
      });
  }
}