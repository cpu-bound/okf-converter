import { Component, inject } from '@angular/core';
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
export class DashboardComponent {
  readonly auth = inject(AuthService);

  private http = inject(HttpClient)

  selectedFile: File | null = null;

  uploading = false;
  uploadProgress = 0;
  uploadComplete = false;
  uploadError = '';

  onFileSelected(event: Event): void {
    const input = event.target as HTMLInputElement;

    const file = input.files?.[0];

    if (!file) {
      return;
    }

    this.selectedFile = file;
    this.uploadComplete = false;
    this.uploadError = '';
    this.uploadProgress = 0;
  }

  uploadFile(file: File): void {
    this.uploading = true;
    this.uploadProgress = 0;
    this.uploadComplete = false;
    this.uploadError = '';

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
                  this.uploadProgress = Math.round(
                    (100 * event.loaded) / event.total
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
          this.http.post(
            `/api/files/${uploadedFile.id}/confirm`,
            {}
          )
        )
      )
      .subscribe({
        next: () => {
          this.uploading = false;
          this.uploadComplete = true;
          this.selectedFile = null;
        },
        error: error => {
          console.error('Upload failed:', error);
          this.uploading = false;
          this.uploadError =
            'Upload failed. Please try again.';
        }
      });
  }
}
