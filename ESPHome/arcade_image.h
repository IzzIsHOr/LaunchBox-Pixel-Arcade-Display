#pragma once
#include "esphome/core/log.h"
#include "esphome/components/display/display.h"
#include "esp_http_client.h"
#include "esp_task_wdt.h"
#include <new>

using namespace esphome;
using namespace esphome::display;

// Canvas 480x320. The proxy streams an RLE-compressed RGB565 image:
//   [W u16 BE][H u16 BE] then runs of [count u16 BE][pixel u16 BE]
// A logo on black compresses massively, so far less data crosses WiFi.
// We expand the runs into block rows and flush them with one draw call per
// block (fast SPI, few set_addr_window round-trips).

static constexpr int CANVAS_W   = 480;
static constexpr int CANVAS_H   = 320;
static constexpr int BLOCK_ROWS = 40;   // 480*40*2 = 38400 bytes per block

struct ArcadeCtx {
  Display *disp;

  // header parsing
  uint8_t  hdr[4];
  int      hdr_got;
  bool     header_done;
  int      img_w, img_h;          // == CANVAS_W x CANVAS_H from proxy

  // RLE run parsing
  uint8_t  runbuf[4];             // [count u16][pixel u16]
  int      run_got;
  uint16_t run_count;             // pixels remaining in current run
  uint16_t run_pixel;             // current run's RGB565 value (BE bytes)

  // output block buffer (full canvas rows)
  uint8_t  block[CANVAS_W * BLOCK_ROWS * 2];
  int      block_start_y;
  int      block_rows;            // full rows currently in block
  int      col;                   // current x within the row being filled
  int      cur_y;                 // current canvas row (0..319)

  bool     drew_any;
  long     pixels_left;           // total pixels still expected (W*H - produced)
};

static void flush_block(ArcadeCtx *ctx) {
  if (ctx->block_rows == 0) return;
  ctx->disp->draw_pixels_at(
    0, ctx->block_start_y, CANVAS_W, ctx->block_rows,
    ctx->block, COLOR_ORDER_RGB, COLOR_BITNESS_565, true, 0, 0, 0);
  ctx->block_rows = 0;
  esp_task_wdt_reset();
}

// Write one pixel (BE RGB565) into the block buffer, advancing x/y.
static void put_pixel(ArcadeCtx *ctx, uint16_t px_be) {
  uint8_t *dst = ctx->block + (ctx->block_rows * CANVAS_W + ctx->col) * 2;
  dst[0] = (uint8_t)(px_be >> 8);
  dst[1] = (uint8_t)(px_be & 0xFF);

  ctx->col++;
  if (ctx->col >= CANVAS_W) {
    ctx->col = 0;
    ctx->block_rows++;
    ctx->cur_y++;
    if (ctx->block_rows >= BLOCK_ROWS) {
      flush_block(ctx);
      ctx->block_start_y = ctx->cur_y;
    }
  }
}

static esp_err_t arcade_on_data(esp_http_client_event_t *evt) {
  if (evt->event_id != HTTP_EVENT_ON_DATA) return ESP_OK;

  ArcadeCtx *ctx  = (ArcadeCtx *)evt->user_data;
  uint8_t   *data = (uint8_t *)evt->data;
  int        len  = evt->data_len;
  int        pos  = 0;

  // 1) header
  if (!ctx->header_done) {
    while (pos < len && ctx->hdr_got < 4)
      ctx->hdr[ctx->hdr_got++] = data[pos++];
    if (ctx->hdr_got < 4) return ESP_OK;

    ctx->img_w = ((uint16_t)ctx->hdr[0] << 8) | ctx->hdr[1];
    ctx->img_h = ((uint16_t)ctx->hdr[2] << 8) | ctx->hdr[3];
    if (ctx->img_w != CANVAS_W || ctx->img_h != CANVAS_H) {
      ESP_LOGE("arcade", "Unexpected size %dx%d", ctx->img_w, ctx->img_h);
      return ESP_FAIL;
    }
    ctx->header_done   = true;
    ctx->drew_any      = true;
    ctx->block_start_y = 0;
    ctx->pixels_left   = (long)CANVAS_W * CANVAS_H;
    ESP_LOGI("arcade", "Image %dx%d (RLE)", ctx->img_w, ctx->img_h);
  }

  // 2) RLE runs
  while (pos < len) {
    // finish reading a 4-byte run header if needed
    if (ctx->run_count == 0) {
      while (pos < len && ctx->run_got < 4)
        ctx->runbuf[ctx->run_got++] = data[pos++];
      if (ctx->run_got < 4) break;            // need more bytes
      ctx->run_count = ((uint16_t)ctx->runbuf[0] << 8) | ctx->runbuf[1];
      ctx->run_pixel = ((uint16_t)ctx->runbuf[2] << 8) | ctx->runbuf[3];
      ctx->run_got   = 0;
      if (ctx->run_count == 0) continue;       // defensive
    }

    // expand the run (bounded by pixels_left)
    while (ctx->run_count > 0 && ctx->pixels_left > 0) {
      put_pixel(ctx, ctx->run_pixel);
      ctx->run_count--;
      ctx->pixels_left--;
    }
    if (ctx->pixels_left <= 0) break;
  }

  return ESP_OK;
}

// Returns true on success (full image received and drawn), false otherwise.
static bool arcade_load_image(const std::string &url, Display *disp) {
  if (url.empty()) return false;
  ESP_LOGI("arcade", "Loading: %s", url.c_str());

  ArcadeCtx *ctx = new (std::nothrow) ArcadeCtx();
  if (ctx == nullptr) { ESP_LOGE("arcade", "Out of memory"); return false; }
  ctx->disp = disp;

  esp_http_client_config_t cfg = {};
  cfg.url           = url.c_str();
  cfg.timeout_ms    = 8000;
  cfg.buffer_size   = 4096;
  cfg.event_handler = arcade_on_data;
  cfg.user_data     = ctx;

  esp_http_client_handle_t client = esp_http_client_init(&cfg);
  esp_err_t err  = esp_http_client_perform(client);
  int       code = esp_http_client_get_status_code(client);
  esp_http_client_cleanup(client);

  bool ok = (err == ESP_OK) && (code == 200) &&
            ctx->header_done && ctx->drew_any && (ctx->pixels_left <= 0);

  if (!ok) {
    if (err != ESP_OK)        ESP_LOGE("arcade", "HTTP failed: %s", esp_err_to_name(err));
    else if (code != 200)     ESP_LOGE("arcade", "HTTP status %d", code);
    else                      ESP_LOGE("arcade", "Incomplete image (%ld px left)", ctx->pixels_left);
    delete ctx;
    return false;
  }

  flush_block(ctx);  // paint any partial trailing block
  ESP_LOGI("arcade", "Done: %dx%d", ctx->img_w, ctx->img_h);
  delete ctx;
  return true;
}
