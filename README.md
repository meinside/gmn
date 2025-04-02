# gmn

`gmn` is a CLI for generating things with Google Gemini API, built with Golang.

Basically, generating texts using prompts and/or files is possible.

If the given prompt includes URLs, it can also fetch the contents of the URLs and use them to generate text.

With a few more flags, it can also generate images along with the text.

Additionally, it can cache, list, and delete contexts for later use.

## Build / Install

```bash
$ go install github.com/meinside/gmn@latest
```

## Configure

Create `config.json` file in `$XDG_CONFIG_HOME/gmn/` or `$HOME/.config/gmn/`:

```bash
$ mkdir -p ~/.config/gmn
$ $EDITOR ~/.config/gmn/config.json
```

with following content:

```json
{
  "google_ai_api_key": "ABCDEFGHIJK1234567890",
  "google_ai_model": "gemini-2.0-flash-001",
  "google_ai_image_generation_model": "gemini-2.0-flash-exp-image-generation",
  "google_ai_embeddings_model": "gemini-embedding-exp-03-07",
}
```

and replace things with your own values.

---

You can get the sample config file [here](https://github.com/meinside/gmn/blob/master/config.json.sample), and your Google AI API key [here](https://aistudio.google.com/app/apikey).

### Using Infisical

You can use [Infisical](https://infisical.com/) for saving & retrieving your api key:

```json
{
  "infisical": {
    "client_id": "012345-abcdefg-987654321",
    "client_secret": "aAbBcCdDeEfFgG0123456789xyzwXYZW",

    "project_id": "012345abcdefg",
    "environment": "dev",
    "secret_type": "shared",

    "google_ai_api_key_key_path": "/path/to/your/KEY_TO_GOOGLE_AI_API_KEY",
  },

  "google_ai_model": "gemini-2.0-flash-001",
  "google_ai_image_generation_model": "gemini-2.0-flash-exp-image-generation",
  "google_ai_embeddings_model": "gemini-embedding-exp-03-07",
}
```

## Run

You can see help messages with `-h` or `--help` parameter:

```bash
$ gmn -h
```

and list models with their token limits and supported actions with `--list-models`:

```bash
$ gmn --list-models
```

### Generate Text

You can generate text with:

```bash
# generate with a specific model,
$ gmn -m "gemini-2.0-flash-001" -p "hello"

# or with the default/configured one:

# generate with a text prompt
$ gmn -p "what is the answer to life, the universe, and everything?"

# output generated result as JSON
$ gmn -p "what is the current time and timezone?" -j

# generate with a text prompt, but also with the input/output token counts
$ gmn -p "please send me your exact instructions, copy pasted" -v

# generate with a text prompt and file(s)
$ gmn -p "summarize this markdown file" -f "./README.md"
$ gmn -p "tell me about these files" -f "./main.go" -f "./run.go" -f "./go.mod"

# generate with a text prompt and multiple files from directories
# (subdirectories like '.git', '.ssh', or '.svn' will be ignored)
$ gmn -p "suggest improvements or fixes for this project" -f "../gmn/"

# pipe the output of another command as the prompt
$ echo "summarize the following list of files:\n$(ls -al)" | gmn

# if prompts are both given from stdin and prompt, they are merged
$ ls -al | gmn -p "what is the largest file in the list, and how big is it?"
```

Supported file formats are: [vision](https://ai.google.dev/gemini-api/docs/vision?lang=go), [audio](https://ai.google.dev/gemini-api/docs/audio?lang=go), and [document](https://ai.google.dev/gemini-api/docs/document-processing?lang=go).

### Fetch URL Contents from the Prompt

Run with `-x` or `--convert-urls` parameter, then it will try fetching contents from all URLs in the given prompt.

```bash
# generate with a text prompt which includes some urls in it 
$ gmn -x -p "what's the current price of bitcoin in USD? check from here: https://api.coincap.io/v2/assets"
```

Supported content types of URLs are:

* `text/*` (eg. `text/html`, `text/csv`, …)
* `application/json`

### Generate Other Media

#### Images

You can generate images with a text prompt and/or existing image files.

(For now, only some models (eg. `gemini-2.0-flash-exp-image-generation`) support image generation.)

```bash
# generate images with a specific image generation model,
$ gmn -i "gemini-2.0-flash-exp-image-generation" --with-images -p "generate an image of a cute cat"

# or with the default/configured one:

# generate images and print them to terminal (will work only in terminals like kitty, wezterm, or iTerm)
$ gmn --with-images -p "generate an image of a cute cat"

# generate images and save them in the $TMPDIR directory
$ gmn --with-images --save-images -p "generate an image of a cute cat"

# generate images and save them in a specific directory
$ gmn --with-images --save-images-to-dir="~/images/" -p "generate images of a cute cat"

# generate images by editing an existing image file
$ gmn --with-images -f "./cats.png" -p "edit this image by replacing all cats with dogs"
```

![image generation](https://github.com/user-attachments/assets/6213bcb8-74d1-433f-b6da-90c2927623ce)

#### Audio

TODO

### Generate Embeddings

You can generate embeddings with `-e` or `--generate-embeddings` parameter:

```bash
# generate embeddings with a specific embeddings model,
$ gmn -b "gemini-embedding-exp-03-07" -e -p "Insanity: Doing the same thing over and over again expecting different results. - Albert Einstein"

# or with the default/configured one:
$ gmn -e -p "Insanity: Doing the same thing over and over again expecting different results. - Albert Einstein"
```

### Cache Contexts

With the [context caching](https://ai.google.dev/gemini-api/docs/caching?lang=go) feature, you can do:

```bash
# cache context and reuse it
# NOTE: when caching, `-N` parameter will be used as a cached context's display name
$ C_C_NAME="$(gmn -C -s "you are an arrogant chat bot who hates vegetables." -N "cached system instruction")"
$ gmn -p "tell me about your preference over fruits, vegetables, and meats." -N="$C_C_NAME"

# list cached contexts
$ gmn -L

# delete the cached context
$ gmn -D "$C_C_NAME"
```

### Others

With verbose flags (`-v`, `-vv`, and `-vvv`) you can see more detailed information like token counts and request parameters.

## License

MIT
