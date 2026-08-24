def execute(data):
    encode = lambda fields: "".join(str(len(x)) + ":" + x for x in fields)
    return {"authorized": encode(data["signed"]) == encode(data["requested"])}
