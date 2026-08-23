import { Component, inject, signal } from '@angular/core';
import {
  FormBuilder,
  ReactiveFormsModule,
  Validators
} from '@angular/forms';
import { Router, RouterLink } from '@angular/router';
import { AuthService } from '../../services/auth.service';

@Component({
  selector: 'app-register',
  standalone: true,
  imports: [ReactiveFormsModule, RouterLink],
  templateUrl: './register.component.html'
})
export class RegisterComponent {
  private fb = inject(FormBuilder);
  private auth = inject(AuthService);
  private router = inject(Router);

  // Signals, not plain fields: error/submitting are set from inside the
  // register() .subscribe() callback, which runs outside any template-bound
  // event - under zoneless change detection a plain field write there
  // would sit unrendered until some unrelated click triggered a check.
  showPassword = signal(false);
  submitting = signal(false);
  error = signal('');

  registerForm = this.fb.nonNullable.group({
    name: ['', [Validators.required, Validators.minLength(2)]],
    email: ['', [Validators.required, Validators.email]],
    password: ['', [Validators.required, Validators.minLength(8)]]
  });

  togglePasswordVisibility(): void {
    this.showPassword.update(shown => !shown);
  }

  submit(): void {
    this.error.set('');

    if (this.registerForm.invalid) {
      this.registerForm.markAllAsTouched();
      return;
    }

    const {
      name,
      email,
      password
    } = this.registerForm.getRawValue();

    this.submitting.set(true);

    this.auth
      .register(
        name,
        email,
        password
      )
      .subscribe({
        next: () => {
          this.router.navigate(['/dashboard']);
        },

        error: error => {
          this.submitting.set(false);
          this.error.set(
            error?.error?.message ||
            'No se pudo crear la cuenta.'
          );
        }
      });
  }
}