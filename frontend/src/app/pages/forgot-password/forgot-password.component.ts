import { Component, inject, signal } from '@angular/core';
import {
  FormBuilder,
  ReactiveFormsModule,
  Validators
} from '@angular/forms';
import { HttpClient } from '@angular/common/http';
import { RouterLink } from '@angular/router';

@Component({
  selector: 'app-forgot-password',
  standalone: true,
  imports: [ReactiveFormsModule, RouterLink],
  templateUrl: './forgot-password.component.html'
})
export class ForgotPasswordComponent {
  private fb = inject(FormBuilder);
  private http = inject(HttpClient);

  // Signals, not plain fields: step/checking/resetting/error are set from
  // inside .subscribe() callbacks, which run outside any template-bound
  // event - under zoneless change detection a plain field write there
  // would sit unrendered until some unrelated click triggered a check.
  step = signal<'email' | 'password' | 'done'>('email');
  checking = signal(false);
  resetting = signal(false);
  error = signal('');

  emailForm = this.fb.nonNullable.group({
    email: ['', [Validators.required, Validators.email]]
  });

  passwordForm = this.fb.nonNullable.group({
    newPassword: ['', [Validators.required, Validators.minLength(8)]],
    confirmPassword: ['', [Validators.required, Validators.minLength(8)]]
  });

  submitEmail(): void {
    this.error.set('');

    if (this.emailForm.invalid) {
      this.emailForm.markAllAsTouched();
      return;
    }

    const { email } = this.emailForm.getRawValue();

    this.checking.set(true);

    this.http
      .post<{ exists: boolean }>('/api/auth/check-email', { email })
      .subscribe({
        next: result => {
          this.checking.set(false);

          if (result.exists) {
            this.step.set('password');
          } else {
            this.error.set('No existe ninguna cuenta con este correo.');
          }
        },
        error: () => {
          this.checking.set(false);
          this.error.set(
            'No se pudo comprobar el correo. Inténtalo de nuevo.'
          );
        }
      });
  }

  submitPassword(): void {
    this.error.set('');

    if (this.passwordForm.invalid) {
      this.passwordForm.markAllAsTouched();
      return;
    }

    const {
      newPassword,
      confirmPassword
    } = this.passwordForm.getRawValue();

    if (newPassword !== confirmPassword) {
      this.error.set('Las contraseñas no coinciden.');
      return;
    }

    const { email } = this.emailForm.getRawValue();

    this.resetting.set(true);

    this.http
      .post('/api/auth/reset-password', { email, newPassword })
      .subscribe({
        next: () => {
          this.resetting.set(false);
          this.step.set('done');
        },
        error: error => {
          this.resetting.set(false);
          this.error.set(
            error?.error?.message ||
            'No se pudo actualizar la contraseña.'
          );
        }
      });
  }
}
