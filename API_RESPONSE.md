# Compiler Endpoint Response Documentation

This document details the behavior and response structures of the `POST /compile` endpoint.

## Endpoint Overview

*   **URL:** `/compile`
*   **Method:** `POST`
*   **Content-Type:** `multipart/form-data`

## Request Parameters

| Key | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `files` | File(s) | **Yes** | The LaTeX source files. **Must include `main.tex`**. Can include multiple files (chapters, images, styles). |
| `artifacts` | String | No | Set to `"true"` to receive a ZIP file containing all workspace artifacts (logs, aux, PDF, sources). Defaults to `"false"`. |

---

## Response Use Cases

### 1. Successful Compilation (Default)
Returns the compiled PDF document. This is the standard behavior when `artifacts` is not set or set to `false`.

*   **Condition:** Compilation succeeds.
*   **Status Code:** `200 OK`
*   **Content-Type:** `application/pdf`
*   **Body:** Binary PDF file (`document.pdf`).

### 2. Successful Compilation (With Artifacts)
Returns a ZIP archive containing the entire workspace. Useful for debugging or retrieving auxiliary files (`.log`, `.aux`) alongside the PDF.

*   **Condition:** Compilation succeeds AND `artifacts="true"`.
*   **Status Code:** `200 OK`
*   **Content-Type:** `application/zip`
*   **Body:** Binary ZIP file (`artifacts.zip`).

### 3. Validation Error
Occurs when the request is malformed or missing required files.

*   **Condition:** `files` key is missing/empty OR `main.tex` is not among the uploaded files.
*   **Status Code:** `400 Bad Request`
*   **Content-Type:** `application/json`
*   **Body Example:**
    ```json
    {
      "error": "main.tex is required"
    }
    ```

### 4. Compilation Failure
Occurs when the LaTeX compiler (`latexmk`) encounters syntax errors that prevent PDF generation. The response includes the compiler logs to help diagnose the issue.

*   **Condition:** `latexmk` returns a non-zero exit code.
*   **Status Code:** `400 Bad Request`
*   **Content-Type:** `application/json`
*   **Body Example:**
    ```json
    {
      "error": "Compilation failed",
      "logs": "Latexmk: This is Latexmk...\n! LaTeX Error: Undefined control sequence.\nl.5 \\sectin{...} ..."
    }
    ```

### 5. Timeout Error
Occurs when the compilation process takes longer than the defined limit (default: 60 seconds).

*   **Condition:** Compilation exceeds 60 seconds.
*   **Status Code:** `408 Request Timeout`
*   **Content-Type:** `application/json`
*   **Body Example:**
    ```json
    {
      "error": "Compilation timed out"
    }
    ```

### 6. Internal Server Error
Occurs due to unexpected system failures, such as filesystem permission issues, failure to create the ZIP archive, or if the PDF is missing despite a successful compiler exit code.

*   **Condition:** System I/O error or unexpected state.
*   **Status Code:** `500 Internal Server Error`
*   **Content-Type:** `application/json`
*   **Body Example:**
    ```json
    {
      "error": "Failed to create zip archive"
    }
    ```
