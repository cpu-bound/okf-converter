import { Component, OnDestroy, inject, signal } from '@angular/core';
import { HttpClient, HttpEventType } from '@angular/common/http';

import { AuthService } from '../../services/auth.service';
import {
  switchMap,
  map,
  tap,
  filter
} from 'rxjs';

@Component({
  selector: 'app-dashboard',
  standalone: true,
  templateUrl: './dashboard.component.html'
})
export class DashboardComponent implements OnDestroy {
  readonly auth = inject(AuthService);

  private http = inject(HttpClient)

  // Every piece of state the template reads is a signal, not a plain
  // field: most of it is written from inside RxJS .subscribe() callbacks
  // (HTTP responses), which run outside of any template-bound event that
  // Angular's zoneless change detection would otherwise notice. A signal
  // write schedules its own re-render; a plain field mutation here would
  // silently sit stale until some unrelated click happened to trigger one.
  selectedFile = signal<File | null>(null);

  uploading = signal(false);
  uploadProgress = signal(0);
  uploadComplete = signal(false);
  uploadError = signal('');

  fileId = signal<string | null>(null);
  bundleUrl = signal<string | null>(null);
  retrying = signal(false);
  copied = signal(false);

  private copiedTimeout: ReturnType<typeof setTimeout> | null = null;

  ngOnDestroy(): void {
    if (this.copiedTimeout !== null) {
      clearTimeout(this.copiedTimeout);
    }
  }

  onFileSelected(event: Event): void {
    const input = event.target as HTMLInputElement;

    const file = input.files?.[0];

    if (!file) {
      return;
    }

    this.selectedFile.set(file);
    this.uploadComplete.set(false);
    this.uploadError.set('');
    this.uploadProgress.set(0);
  }

  // The download now goes through the API (GET /api/files/:id/bundle), which
  // only serves a bundle that passed validation and belongs to the caller.
  // Until then the endpoint answers 409 with the reason, so this UI still
  // has no status tracking of its own - clicking the link too early explains
  // itself rather than downloading. Polling and a gated button belong to the
  // next change.
  uploadFile(file: File): void {
    this.uploading.set(true);
    this.uploadProgress.set(0);
    this.uploadComplete.set(false);
    this.uploadError.set('');
    this.fileId.set(null);
    this.bundleUrl.set(null);

    this.http
      .post<{
        file: {
          id: string;
          original_name: string;
          content_type: string;
          size: number;
        };
        uploadUrl: string;
      }>('/api/files/upload-url', {
        filename: file.name,
        contentType:
          file.type || 'application/octet-stream',
        size: file.size
      })
      .pipe(
        switchMap(result =>
          this.http
            .put(result.uploadUrl, file, {
              headers: {
                'Content-Type':
                  file.type ||
                  'application/octet-stream'
              },
              reportProgress: true,
              observe: 'events',
              responseType: 'text'
            })
            .pipe(
              tap(event => {
                if (
                  event.type ===
                    HttpEventType.UploadProgress &&
                  event.total
                ) {
                  this.uploadProgress.set(
                    Math.round(
                      (100 * event.loaded) / event.total
                    )
                  );
                }
              }),
              filter(
                event =>
                  event.type === HttpEventType.Response
              ),
              map(() => result.file)
            )
        ),
        switchMap(uploadedFile =>
          this.http.post<{
            file: { id: string };
          }>(`/api/files/${uploadedFile.id}/confirm`, {})
        )
      )
      .subscribe({
        next: confirmed => {
          this.uploading.set(false);
          this.uploadComplete.set(true);
          this.selectedFile.set(null);
          this.fileId.set(confirmed.file.id);
          this.bundleUrl.set(this.bundleUrlFor(confirmed.file.id));
        },
        error: error => {
          console.error('Upload failed:', error);
          this.uploading.set(false);
          this.uploadError.set(
            'Error al subir el archivo. Inténtalo de nuevo.'
          );
        }
      });
  }

  // The download URL is a pure function of the file id, so it never has to
  // be handed back by the API or refreshed: it is the same address before,
  // during and after the conversion - what changes is what the API answers
  // there.
  private bundleUrlFor(fileId: string): string {
    return `${location.origin}/api/files/${fileId}/bundle`;
  }

  // A manual, user-triggered action (not the result of the frontend watching
  // the file's status) - if the download didn't work, the user can ask for a
  // fresh conversion attempt.
  retryConversion(): void {
    const fileId = this.fileId();
    if (!fileId) {
      return;
    }

    this.retrying.set(true);

    this.http
      .post<{
        file: { id: string };
      }>(`/api/files/${fileId}/retry`, {})
      .subscribe({
        next: retried => {
          this.retrying.set(false);
          this.bundleUrl.set(this.bundleUrlFor(retried.file.id));
        },
        error: error => {
          console.error('Retry failed:', error);
          this.retrying.set(false);
        }
      });
  }

  copyBundleUrl(): void {
    const url = this.bundleUrl();
    if (!url) {
      return;
    }

    navigator.clipboard
      .writeText(url)
      .then(() => {
        this.copied.set(true);
        if (this.copiedTimeout !== null) {
          clearTimeout(this.copiedTimeout);
        }
        this.copiedTimeout = setTimeout(
          () => this.copied.set(false),
          2000
        );
      })
      .catch(error => {
        console.error('Copy failed:', error);
      });
  }

  selectUrlInput(event: Event): void {
    (event.target as HTMLInputElement).select();
  }

  formatFileSize(bytes: number): string {
    if (bytes < 1024) {
      return `${bytes} B`;
    }
    if (bytes < 1024 * 1024) {
      return `${(bytes / 1024).toFixed(1)} KB`;
    }
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }
}
