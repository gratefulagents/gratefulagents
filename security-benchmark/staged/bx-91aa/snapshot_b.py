def execute(data):
    if data["items"] > data["cap"]:
        return {"work": 0}
    return {"work": data["items"]}
