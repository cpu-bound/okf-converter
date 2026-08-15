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
  resultUrl = signal<string | null>(null);
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

  // The frontend's job ends the moment it has the presigned download URL -
  // there is no follow-up status call. The backend hands that URL back in
  // the same confirm response that pushes the conversion job, before the
  // job has even run, so there is nothing left for the client to track:
  // the URL itself, tried whenever the user chooses, is the only signal of
  // "is it ready" this UI needs.
  uploadFile(file: File): void {
    this.uploading.set(true);
    this.uploadProgress.set(0);
    this.uploadComplete.set(false);
    this.uploadError.set('');
    this.fileId.set(null);
    this.resultUrl.set(null);

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
            resultUrl: string;
          }>(`/api/files/${uploadedFile.id}/confirm`, {})
        )
      )
      .subscribe({
        next: confirmed => {
          this.uploading.set(false);
          this.uploadComplete.set(true);
          this.selectedFile.set(null);
          this.fileId.set(confirmed.file.id);
          this.resultUrl.set(confirmed.resultUrl);
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

  // A manual, user-triggered action (not the result of the frontend
  // watching the file's status) - if the download link didn't work, the
  // user can ask for a fresh conversion attempt. The retry response is the
  // same shape as confirm's: a resultUrl handed back immediately, not a
  // status to keep watching.
  retryConversion(): void {
    const fileId = this.fileId();
    if (!fileId) {
      return;
    }

    this.retrying.set(true);

    this.http
      .post<{
        file: { id: string };
        resultUrl: string;
      }>(`/api/files/${fileId}/retry`, {})
      .subscribe({
        next: retried => {
          this.retrying.set(false);
          this.resultUrl.set(retried.resultUrl);
        },
        error: error => {
          console.error('Retry failed:', error);
          this.retrying.set(false);
        }
      });
  }

  copyResultUrl(): void {
    const url = this.resultUrl();
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
