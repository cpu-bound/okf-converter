import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpEvent } from '@angular/common/http';
import { Observable, map } from 'rxjs';

// The states a file moves through. 'pending' means the upload was never
// confirmed, so no worker will ever pick it up; 'ready' and 'converting' are
// the only ones worth polling.
export type FileStatus =
  | 'pending'
  | 'ready'
  | 'converting'
  | 'converted'
  | 'failed';

// How the bundle built for a file was classified. Absent until a conversion
// has produced a bundle to classify.
export type Verdict = 'valid' | 'valid_with_warnings' | 'invalid';

export interface UserFile {
  id: string;
  original_name: string;
  content_type: string;
  size: number;
  status: FileStatus;
  validation?: Verdict;
  created_at: string;
}

export interface ValidationCheck {
  id: string;
  scope: 'platform' | 'okf';
  rule: string;
  severity: 'error' | 'warning';
  passed: boolean;
  details?: string[];
}

export interface ValidationReport {
  verdict: Verdict;
  platform: Verdict;
  okf: Verdict;
  checks: ValidationCheck[];
}

export interface FileDetail {
  file: UserFile;
  report: ValidationReport | null;
}

// What the file picker offers. Both extensions and MIME types are listed:
// browsers match on either, and the backend accepts a document if *either*
// its declared content type or its extension is recognised, so anything this
// list lets through is something the pipeline can actually convert.
export const ACCEPTED_UPLOADS =
  '.md,.markdown,.html,.htm,.txt,.csv,.pdf,' +
  'text/markdown,text/html,text/plain,text/csv,application/pdf';

@Injectable({ providedIn: 'root' })
export class FilesService {
  private http = inject(HttpClient);

  // The caller's own files, newest first. One request covers every file the
  // dashboard is tracking, which is why polling this beats polling each file.
  list(): Observable<UserFile[]> {
    return this.http
      .get<{ files: UserFile[] }>('/api/files')
      .pipe(map(response => response.files));
  }

  // One file plus the rule-by-rule validation report, which the list
  // deliberately leaves out because it is only needed when the user opens a
  // file's detail.
  detail(fileId: string): Observable<FileDetail> {
    return this.http
      .get<{
        file: UserFile;
        validation_report?: ValidationReport;
      }>(`/api/files/${fileId}`)
      .pipe(
        map(response => ({
          file: response.file,
          report: response.validation_report ?? null
        }))
      );
  }

  requestUpload(file: File): Observable<{
    file: UserFile;
    uploadUrl: string;
  }> {
    return this.http.post<{
      file: UserFile;
      uploadUrl: string;
    }>('/api/files/upload-url', {
      filename: file.name,
      contentType: file.type || 'application/octet-stream',
      size: file.size
    });
  }

  // The browser uploads straight to object storage with the presigned URL,
  // so the bytes never pass through the API.
  uploadToStorage(
    uploadUrl: string,
    file: File
  ): Observable<HttpEvent<string>> {
    return this.http.put(uploadUrl, file, {
      headers: {
        'Content-Type': file.type || 'application/octet-stream'
      },
      reportProgress: true,
      observe: 'events',
      responseType: 'text'
    });
  }

  confirm(fileId: string): Observable<UserFile> {
    return this.http
      .post<{ file: UserFile }>(`/api/files/${fileId}/confirm`, {})
      .pipe(map(response => response.file));
  }

  retry(fileId: string): Observable<UserFile> {
    return this.http
      .post<{ file: UserFile }>(`/api/files/${fileId}/retry`, {})
      .pipe(map(response => response.file));
  }

  // A pure function of the file id: the download address is the same before,
  // during and after the conversion. What changes is what the API answers
  // there - it only serves a bundle that passed validation and belongs to
  // the caller.
  bundleUrl(fileId: string): string {
    return `${location.origin}/api/files/${fileId}/bundle`;
  }
}
