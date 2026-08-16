import {
  Component,
  OnDestroy,
  OnInit,
  computed,
  inject,
  signal
} from '@angular/core';
import { HttpEventType } from '@angular/common/http';
import { filter, map, switchMap, tap } from 'rxjs';

import { AuthService } from '../../services/auth.service';
import {
  ACCEPTED_UPLOADS,
  FilesService,
  UserFile,
  ValidationReport,
  Verdict
} from '../../services/files.service';

// A file in one of these states is waiting on a worker, so the dashboard
// keeps asking. Anything else is final as far as the client is concerned -
// including 'pending', which means the upload was never confirmed and no
// worker will ever pick it up.
const ACTIVE_STATUSES = ['ready', 'converting'];

const POLL_INTERVAL_MS = 2500;

// Polling stops after this long without everything settling, so a worker that
// died doesn't leave a browser tab asking forever. The user can start it
// again with the refresh button.
const MAX_POLL_MS = 10 * 60 * 1000;

@Component({
  selector: 'app-dashboard',
  standalone: true,
  templateUrl: './dashboard.component.html'
})
export class DashboardComponent implements OnInit, OnDestroy {
  readonly auth = inject(AuthService);

  private files = inject(FilesService);

  readonly acceptedUploads = ACCEPTED_UPLOADS;

  // Every piece of state the template reads is a signal, not a plain field:
  // most of it is written from inside RxJS .subscribe() callbacks (HTTP
  // responses), which run outside of any template-bound event that Angular's
  // zoneless change detection would otherwise notice. A signal write
  // schedules its own re-render; a plain field mutation here would silently
  // sit stale until some unrelated click happened to trigger one.
  selectedFile = signal<File | null>(null);

  uploading = signal(false);
  uploadProgress = signal(0);
  uploadError = signal('');

  myFiles = signal<UserFile[]>([]);
  listError = signal('');
  polling = signal(false);
  pollingGaveUp = signal(false);

  // Which file's validation detail is open, and the reports fetched so far.
  // Reports are kept out of the polled list response (they would be re-sent
  // every couple of seconds) and fetched once, on demand.
  expandedId = signal<string | null>(null);
  reports = signal<Record<string, ValidationReport>>({});
  reportError = signal('');

  retryingId = signal<string | null>(null);

  readonly hasActiveFiles = computed(() =>
    this.myFiles().some(file => ACTIVE_STATUSES.includes(file.status))
  );

  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private pollingStartedAt = 0;

  ngOnInit(): void {
    this.refresh();
  }

  ngOnDestroy(): void {
    this.stopPolling();
  }

  // --- listing and polling --------------------------------------------------

  refresh(): void {
    this.pollingGaveUp.set(false);
    this.loadFiles(true);
  }

  private loadFiles(mayStartPolling: boolean): void {
    this.files.list().subscribe({
      next: files => {
        this.myFiles.set(files);
        this.listError.set('');

        if (this.hasActiveFiles()) {
          if (mayStartPolling) {
            this.startPolling();
          }
        } else {
          this.stopPolling();
        }
      },
      error: error => {
        console.error('No se pudo cargar la lista de archivos:', error);
        this.listError.set('No se pudo cargar la lista de archivos.');
        // Polling on top of a failing request would just multiply the
        // failures; the user can retry deliberately.
        this.stopPolling();
      }
    });
  }

  private startPolling(): void {
    if (this.pollTimer !== null) {
      return;
    }

    this.pollingStartedAt = Date.now();
    this.polling.set(true);

    this.pollTimer = setInterval(() => {
      if (Date.now() - this.pollingStartedAt > MAX_POLL_MS) {
        this.stopPolling();
        this.pollingGaveUp.set(true);
        return;
      }
      this.loadFiles(false);
    }, POLL_INTERVAL_MS);
  }

  private stopPolling(): void {
    if (this.pollTimer !== null) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
    this.polling.set(false);
  }

  // --- upload ---------------------------------------------------------------

  onFileSelected(event: Event): void {
    const input = event.target as HTMLInputElement;
    const file = input.files?.[0];

    if (!file) {
      return;
    }

    this.selectedFile.set(file);
    this.uploadError.set('');
    this.uploadProgress.set(0);

    // Without this the same file picked twice in a row fires no change event.
    input.value = '';
  }

  clearSelection(): void {
    this.selectedFile.set(null);
    this.uploadProgress.set(0);
  }

  uploadFile(file: File): void {
    this.uploading.set(true);
    this.uploadProgress.set(0);
    this.uploadError.set('');

    this.files
      .requestUpload(file)
      .pipe(
        switchMap(requested =>
          this.files.uploadToStorage(requested.uploadUrl, file).pipe(
            tap(event => {
              if (
                event.type === HttpEventType.UploadProgress &&
                event.total
              ) {
                this.uploadProgress.set(
                  Math.round((100 * event.loaded) / event.total)
                );
              }
            }),
            filter(event => event.type === HttpEventType.Response),
            map(() => requested.file)
          )
        ),
        switchMap(uploaded => this.files.confirm(uploaded.id))
      )
      .subscribe({
        next: () => {
          this.uploading.set(false);
          this.selectedFile.set(null);
          this.uploadProgress.set(0);
          // The file is now queued, so the list has something to track.
          this.refresh();
        },
        error: error => {
          console.error('La subida falló:', error);
          this.uploading.set(false);
          this.uploadError.set(this.uploadErrorMessage(error));
        }
      });
  }

  // The API explains rejections in Spanish (unsupported format, size
  // mismatch, expired link), so show what it said rather than replacing it
  // with a generic message that hides the actual reason.
  private uploadErrorMessage(error: unknown): string {
    const message = (error as { error?: { error?: string } })?.error?.error;
    return message || 'Error al subir el archivo. Inténtalo de nuevo.';
  }

  // --- per-file actions -----------------------------------------------------

  isDownloadable(file: UserFile): boolean {
    return file.status === 'converted';
  }

  bundleUrl(file: UserFile): string {
    return this.files.bundleUrl(file.id);
  }

  retryConversion(file: UserFile): void {
    this.retryingId.set(file.id);

    this.files.retry(file.id).subscribe({
      next: () => {
        this.retryingId.set(null);
        this.refresh();
      },
      error: error => {
        console.error('El reintento falló:', error);
        this.retryingId.set(null);
      }
    });
  }

  toggleDetail(file: UserFile): void {
    if (this.expandedId() === file.id) {
      this.expandedId.set(null);
      return;
    }

    this.expandedId.set(file.id);
    this.reportError.set('');

    if (this.reports()[file.id]) {
      return;
    }

    this.files.detail(file.id).subscribe({
      next: detail => {
        if (detail.report) {
          this.reports.update(current => ({
            ...current,
            [file.id]: detail.report as ValidationReport
          }));
        }
      },
      error: error => {
        console.error('No se pudo cargar el informe de validación:', error);
        this.reportError.set('No se pudo cargar el informe de validación.');
      }
    });
  }

  reportFor(file: UserFile): ValidationReport | null {
    return this.reports()[file.id] ?? null;
  }

  failedChecks(report: ValidationReport) {
    return report.checks.filter(check => !check.passed);
  }

  // --- labels ---------------------------------------------------------------

  statusLabel(status: string): string {
    switch (status) {
      case 'pending':
        return 'Subida sin confirmar';
      case 'ready':
        return 'En cola';
      case 'converting':
        return 'Convirtiendo';
      case 'converted':
        return 'Bundle publicado';
      case 'failed':
        return 'Falló';
      default:
        return status;
    }
  }

  verdictLabel(verdict: Verdict | undefined): string {
    switch (verdict) {
      case 'valid':
        return 'Válido';
      case 'valid_with_warnings':
        return 'Válido con advertencias';
      case 'invalid':
        return 'Inválido';
      default:
        return '';
    }
  }

  scopeLabel(scope: string): string {
    return scope === 'okf' ? 'OKF' : 'Plataforma';
  }

  severityLabel(severity: string): string {
    return severity === 'warning' ? 'Advertencia' : 'Error';
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

  formatDate(iso: string): string {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) {
      return '';
    }
    return date.toLocaleString('es-CO', {
      dateStyle: 'medium',
      timeStyle: 'short'
    });
  }
}
