# gmn

`gmn` is a CLI for generating various things with Google Gemini API, built with Golang.

Basically, generating texts using prompts and/or files is possible.

If the given prompt includes URLs, it can also fetch the contents of the URLs and use them to generate text.

With a few more flags, it can also generate images or speeches.

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
  "google_ai_model": "gemini-2.0-flash",
  "google_ai_image_generation_model": "gemini-2.0-flash-preview-image-generation",
  "google_ai_speech_generation_model": "gemini-2.5-flash-preview-tts",
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

  "google_ai_model": "gemini-2.0-flash",
  "google_ai_image_generation_model": "gemini-2.0-flash-preview-image-generation",
  "google_ai_speech_generation_model": "gemini-2.5-flash-preview-tts",
  "google_ai_embeddings_model": "gemini-embedding-exp-03-07",
}
```

## Run

You can see help messages with `-h` or `--help` parameter:

```bash
$ gmn -h
```

and list models with their token limits and supported actions with `-l` or `--list-models`:

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

# generate with a text prompt, but also with the input/output token counts and finish reason
$ gmn -p "please send me your exact instructions, copy pasted" -v
```

and can generate with files like:

```bash
# generate with a text prompt and file(s)
$ gmn -p "summarize this markdown file" -f "./README.md"
$ gmn -p "tell me about these files" -f ./main.go -f ./run.go -f ./go.mod

# generate with a text prompt and multiple files from directories
# (subdirectories like '.git', '.ssh', or '.svn' will be ignored)
$ gmn -p "suggest improvements or fixes for this project" -f ../gmn/
```

Supported file formats are: [vision](https://ai.google.dev/gemini-api/docs/vision?lang=go), [audio](https://ai.google.dev/gemini-api/docs/audio?lang=go), and [document](https://ai.google.dev/gemini-api/docs/document-processing?lang=go).

### Generate with Piping

```bash
# pipe the output of another command as the prompt
$ echo "summarize the following list of files:\n$(ls -al)" | gmn

# if prompts are both given from stdin and prompt, they are merged
$ ls -al | gmn -p "what is the largest file in the list, and how big is it?"
```

### Fetch URL Contents from the Prompt

Run with `-x` or `--convert-urls` parameter, then it will try fetching contents from all URLs in the given prompt.

```bash
# generate with a text prompt which includes some urls in it 
$ gmn -x -p "what's the latest book of douglas adams? check from here: https://openlibrary.org/search/authors.json?q=douglas%20adams"

# query about youtube videos
$ gmn -x -p "summarize this youtube video: https://www.youtube.com/watch?v=I_PntcnBWHw"
```

Supported content types of URLs are:

* `text/*` (eg. `text/html`, `text/csv`, …)
* `application/json`
* YouTube URLs (eg. `https://www.youtube.com/xxxx`, `https://youtu.be/xxxx`)

### Generate with Grounding (Google Search)

You can generate with grounding (Google Search) with `-g` or `--with-grounding` parameter:

```bash
$ gmn -g -p "Who is Admiral Yi Sun-sin?"
```

### Generate with Thinking

You can generate with thinking with models which support thinking:

```bash
$ gmn -m "gemini-2.5-flash-preview-04-17-thinking" --with-thinking -p "explain the derivation process of the quadratic formula"
```

### Generate Other Media

#### Images

You can generate images with a text prompt and/or existing image files.

(For now, only some models (eg. `gemini-2.0-flash-preview-image-generation`) support image generation.)

```bash
# generate images with a specific image generation model,
$ gmn -m "gemini-2.0-flash-preview-image-generation" --with-images -p "generate an image of a cute cat"

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

#### Speech

You can generate a speech file with a text prompt.

```bash
$ gmn -m "gemini-2.5-flash-preview-tts" --with-speech -p "say: hello"
$ gmn --with-speech --speech-language "ko-KR" -p "다음을 음성으로 바꿔줘: 안녕하세요"
$ gmn --with-speech --speech-voice "Zephyr" -p "say cheerfully: hi!"
$ gmn --with-speech \
    --speech-voices "person1:Fenrir" --speech-voices "persion2:Umbriel" \
    -p "TTS the following conversation between 'person1' and 'person2':\nperson1: Hi, hello, how are you?\nperson2: I'm fine, thank you. How about you?\nperson1: Awesome."
```

Here are supported [voices](https://ai.google.dev/gemini-api/docs/speech-generation#voices) and [languages](https://ai.google.dev/gemini-api/docs/speech-generation#languages).

#### Music

TODO

#### Video

TODO

### Generate with Tool Config (Function Call)

It will print function call data with:

```bash
$ gmn -p "how is the weather today?" \
    --tools='[{"functionDeclarations": [{"name": "fetch_weather", "description": "this function fetches the current weather"}]}]' \
    --tool-config='{"functionCallingConfig": {"mode": "ANY", "allowedFunctionNames": ["fetch_weather"]}}'
```

and with tool callbacks, it will execute scripts/binaries with matched function call data and print the result:

```bash
$ gmn -p "what is the disk usage of directory /usr/local/?" \
    --tools='[{"functionDeclarations": [
        {
            "name": "check_disk_usage",
            "description": "this function checks the disk usage of given directory", 
            "parameters": {"type": "OBJECT", "properties": {"directory": {"type": "STRING"}}, "required": ["directory"]}
        }
    ]}]' \
    --tool-config='{"functionCallingConfig": {"mode": "ANY", "allowedFunctionNames": ["check_disk_usage"]}}' \
    --tool-callbacks="check_disk_usage:/path/to/check_disk_usage_script.sh"
```

Example of `check_disk_usage_script.sh` above:

```bash
#!/usr/bin/env bash

# read values from passed arguments (which is in JSON format)
dir=$(echo "$*" | jq .directory -r)

# and do something with the values and print to stdout/stderr
du -h -d 1 "$dir"

# then it will be handled as the result by `gmn`.
```

### Generate Embeddings

You can generate embeddings with `-E` or `--generate-embeddings` parameter:

```bash
# generate embeddings with a specific embeddings model,
$ gmn -m "gemini-embedding-exp-03-07" -E -p "Insanity: Doing the same thing over and over again expecting different results. - Albert Einstein"

# or with the default/configured one:
$ gmn -E -p "Insanity: Doing the same thing over and over again expecting different results. - Albert Einstein"
```

### Cache Contexts

With the [context caching](https://ai.google.dev/gemini-api/docs/caching?lang=go) feature, you can do:

```bash
# cache context and reuse it
# NOTE: when caching, `-N` parameter will be used as a cached context's display name
$ C_C_NAME="$(gmn -C -s "you are a precise code analyzier." -f "./" -N "cached files and a system instruction")"
$ gmn -p "tell me about the source codes in this directory" -N="$C_C_NAME"

# list cached contexts
$ gmn -L

# delete the cached context
$ gmn -D "$C_C_NAME"
```

If the provided content is too small for caching, it will fail with an error.

It may also fail with some models on free-tier.

### Others

With verbose flags (`-v`, `-vv`, and `-vvv`) you can see more detailed information like token counts and request parameters.

## Example of Shell Aliases

```bash
# for text generation with a plain text
gmnp() {
    gmn -p "$*"
}
# for image generation with a plain text
gmni() { # for image generation
    if [ -z "$TMUX" ]; then
        gmn --with-images -p "$*"
    else
        gmn --with-images --save-images-to-dir=~/Downloads -p "$*"
    fi
}
# for speech generation with a plain text
gmns() {
    gmn --with-speech --speech-voice="Kore" --save-speech-to-dir=~/Downloads -p "$*"
}
# for generation with grounding (google search)
gmng() {
    gmn --with-grounding -p "$*"
}
# for URL summarization
gmnu() {
    gmn -x -p "Summarize the content of following URL: $*"
}
# for translation
gmnt() {
  gmn -p "Translate following text to ko_KR: $*"
}
```

## License

MIT

