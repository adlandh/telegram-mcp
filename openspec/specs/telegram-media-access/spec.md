# telegram-media-access Specification

## Purpose

Inspect Telegram message media and safely save full files or lightweight previews within a configurable local download directory.

## Requirements

### Requirement: Inspect media without downloading

`get_media_info` SHALL describe available media type, MIME type, size, duration, filename, and downloadability without fetching the file payload. Supported downloadable categories SHALL include photos, documents, images, videos, audio, and voice messages. A message without media SHALL produce an explanatory result; unsupported media SHALL NOT be described as downloadable.

#### Scenario: Video metadata does not download the video

- **WHEN** a caller requests media information for a video message
- **THEN** the response describes its metadata without downloading its payload

### Requirement: Bounded full-media downloads

`download_media` SHALL save supported media under `TELEGRAM_DOWNLOAD_DIR` and return its path and actual size. The effective limit SHALL default to `TELEGRAM_MAX_DOWNLOAD_MB` and SHALL be overridden by `maxMB` for that call; zero SHALL mean unlimited. A nonzero limit SHALL be enforced both against available size metadata before transfer and against bytes actually written. Files stored in a Telegram data center different from the account's current data center SHALL be fetched from the appropriate location.

#### Scenario: Default size limit rejects oversized media

- **WHEN** a message reports a 201 MiB payload and the configured default limit is 200 MiB
- **THEN** the server rejects the download before transfer

#### Scenario: Actual payload size exceeds metadata

- **WHEN** a transfer writes more bytes than its reported metadata size and exceeds the effective limit
- **THEN** the server removes the partial file and returns an error

#### Scenario: Unlimited per-call download remains cancellable

- **WHEN** a caller sets `maxMB=0`
- **THEN** the size limit is disabled for that call while request deadlines still apply

### Requirement: Lightweight thumbnails

`get_thumbnail` SHALL save only a preview rather than the full media payload. It SHALL select the largest available raster preview with both dimensions at most 320 pixels, falling back to an embedded stripped thumbnail when no such raster exists. Embedded cached or stripped previews SHALL be handled without a full-file download. If no suitable preview exists, the tool SHALL report an error. The configured size limit SHALL apply to preview bytes, not the original file size.

#### Scenario: Large video uses a small preview

- **WHEN** a 1 GiB video has an 18 KiB suitable preview
- **THEN** `get_thumbnail` saves the preview without transferring the full video

#### Scenario: Preview is unavailable

- **WHEN** no suitable preview exists
- **THEN** `get_thumbnail` returns an error without transferring the full file

### Requirement: Respect protected content

Both download tools SHALL reject media when the resolved chat or target message restricts saving with a no-forward flag. Missing messages, unavailable media, and unsupported payload types SHALL yield descriptive failures rather than successful empty files.

#### Scenario: Protected channel media is not saved

- **WHEN** media belongs to a protected channel or message
- **THEN** neither download tool creates a file

### Requirement: Private and isolated local files

Downloads SHALL sanitize untrusted filename components, stay inside the configured directory, create unique owner-only 0600 files, and not overwrite earlier downloads with the same source name. Newly created download directories SHALL use permissions 0700. Successful results SHALL reference completed files; failed transfers SHALL remove partial files during normal error handling.

#### Scenario: Repeated unsafe filename creates separate private files

- **WHEN** two downloads use the same unsafe source filename
- **THEN** the server saves distinct sanitized files with 0600 permissions

#### Scenario: Interrupted transfer removes partial file

- **WHEN** a transfer is interrupted
- **THEN** its partial file is removed during normal error handling
